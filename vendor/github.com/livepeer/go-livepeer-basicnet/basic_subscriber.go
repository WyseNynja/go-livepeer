package basicnet

import (
	"context"
	"errors"
	"fmt"
	"time"

	kb "gx/ipfs/QmSAFA8v42u4gpJNy1tb7vW3JiiXiaYDC2b845c2RnNSJL/go-libp2p-kbucket"
	peer "gx/ipfs/QmXYjuNuxVzXKJCfWasQk1RqkhVLDM9jtUKhqc2WPQmFSB/go-libp2p-peer"
	host "gx/ipfs/Qmc1XhrFEiSeBNn3mpfg6gEuYCt5im2gYmNVmncsvmpeAk/go-libp2p-host"

	"github.com/golang/glog"
	"github.com/livepeer/go-livepeer/common"
)

var SubscriberDataInsertTimeout = time.Second * 300
var ErrSubscriber = errors.New("ErrSubscriber")

//BasicSubscriber keeps track of
type BasicSubscriber struct {
	Network       *BasicVideoNetwork
	host          host.Host
	msgChan       chan StreamDataMsg
	networkStream *BasicStream
	StrmID        string
	UpstreamPeer  peer.ID
	working       bool
	cancelWorker  context.CancelFunc
}

//Subscribe kicks off a go routine that calls the gotData func for every new video chunk
func (s *BasicSubscriber) Subscribe(ctx context.Context, gotData func(seqNo uint64, data []byte, eof bool)) error {
	//Do we already have the broadcaster locally? If we do, just subscribe to it and listen.
	if b := s.Network.broadcasters[s.StrmID]; b != nil {
		glog.V(4).Infof("Broadcaster is present, let's return an error for now")
		//TODO: read from broadcaster
		return ErrSubscriber
	}

	//If we don't, send subscribe request, listen for response
	localPeers := s.Network.NetworkNode.PeerHost.Peerstore().Peers()
	if len(localPeers) == 1 {
		glog.Errorf("No local peers")
		return ErrSubscriber
	}
	targetPid, err := extractNodeID(s.StrmID)
	if err != nil {
		glog.Errorf("Error extracting node id from streamID: %v", s.StrmID)
		return ErrSubscriber
	}
	peers := kb.SortClosestPeers(localPeers, kb.ConvertPeerID(targetPid))

	for _, p := range peers {
		if p == s.Network.NetworkNode.Identity {
			continue
		}
		//Question: Where do we close the stream? If we only close on "Unsubscribe", we may leave some streams open...
		glog.V(5).Infof("New peer from kademlia: %v", peer.IDHexEncode(p))
		ns := s.Network.NetworkNode.GetStream(p)
		if ns != nil {
			//Send SubReq
			glog.Infof("Sending Req %v", s.StrmID)
			if err := ns.SendMessage(SubReqID, SubReqMsg{StrmID: s.StrmID}); err != nil {
				glog.Errorf("Error sending SubReq to %v: %v", peer.IDHexEncode(p), err)
			}
			ctxW, cancel := context.WithCancel(context.Background())
			s.cancelWorker = cancel
			s.working = true
			s.networkStream = ns
			s.UpstreamPeer = p
			s.startWorker(ctxW, p, ns, gotData)
			return nil
		}
	}

	glog.Errorf("Cannot subscribe from any of the peers: %v", peers)
	return ErrNoClosePeers

	//Call gotData for every new piece of data
}

func (s *BasicSubscriber) startWorker(ctxW context.Context, p peer.ID, ws *BasicStream, gotData func(seqNo uint64, data []byte, eof bool)) {
	//We expect DataStreamMsg to come back
	go func() {
		for {
			//Get message from the msgChan (inserted from the network by StreamDataMsg)
			//Call gotData(seqNo, data)
			//Question: What happens if the handler gets stuck?
			start := time.Now()
			select {
			case msg := <-s.msgChan:
				networkWaitTime := time.Since(start)
				go gotData(msg.SeqNo, msg.Data, false)
				glog.V(common.DEBUG).Infof("Subscriber worker inserted segment: %v - took %v in total, %v waiting for data", msg.SeqNo, time.Since(start), networkWaitTime)
			case <-ctxW.Done():
				s.networkStream = nil
				s.working = false
				glog.Infof("Done with subscription, sending CancelSubMsg")
				//Send EOF
				go gotData(0, nil, true)
				if err := ws.SendMessage(CancelSubID, CancelSubMsg{StrmID: s.StrmID}); err != nil {
					glog.Errorf("Error sending CancelSubMsg during worker cancellation: %v", err)
				}
				return
			}
		}
	}()
}

//Unsubscribe unsubscribes from the broadcast
func (s *BasicSubscriber) Unsubscribe() error {
	if s.cancelWorker != nil {
		s.cancelWorker()
	}
	//Remove self from network
	delete(s.Network.subscribers, s.StrmID)

	return nil
}

func (s BasicSubscriber) String() string {
	return fmt.Sprintf("StreamID: %v, working: %v", s.StrmID, s.working)
}

func (s *BasicSubscriber) IsWorking() bool {
	return s.working
}
