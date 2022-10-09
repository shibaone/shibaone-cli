/*
Copyright © 2022 Polygon <engineering@polygon.technology>

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Lesser General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU Lesser General Public License for more details.

You should have received a copy of the GNU Lesser General Public License
along with this program. If not, see <http://www.gnu.org/licenses/>.
*/
package cmd

import (
	"bufio"
	"context"
	"crypto/sha1"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	// ds "github.com/ipfs/go-datastore"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/routing"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"
	dutil "github.com/libp2p/go-libp2p/p2p/discovery/util"
	"github.com/libp2p/go-libp2p/p2p/muxer/mplex"
	"github.com/libp2p/go-libp2p/p2p/muxer/yamux"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	tls "github.com/libp2p/go-libp2p/p2p/security/tls"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	"github.com/libp2p/go-libp2p/p2p/transport/websocket"
	ma "github.com/multiformats/go-multiaddr"
	mh "github.com/multiformats/go-multihash"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

// dhtCmd represents the dht command
var dhtCmd = &cobra.Command{
	Use:   "dht",
	Short: "Basic testing and diagnostics for Kademlia DHT",
	Long: `The goal of this command is to have some basic utilities for
connecting to Kademlia DHT. Ideally we could connect and provide
detailed tracing an analysis based on the messages that we're
receiving. Additionally we might want to be able to execute arbitrary
commands like ping, store, get, and lookup.
`,
	RunE: AvailDHT,
}

// https://docs.rs/ipfs-embed/latest/src/ipfs_embed/net/config.rs.html#67
const dhtProtocol = "/ipfs-embed/1.0"

func AvailDHT(cmd *cobra.Command, args []string) error {
	zerolog.SetGlobalLevel(zerolog.TraceLevel)
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	transports := libp2p.ChainOptions(
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.Transport(websocket.New),
	)

	muxers := libp2p.ChainOptions(
		libp2p.Muxer("/yamux/1.0.0", yamux.DefaultTransport),
		libp2p.Muxer("/mplex/6.7.0", mplex.DefaultTransport),
	)

	security := libp2p.Security(tls.ID, tls.New)
	_ = security
	securityNoise := libp2p.Security(noise.ID, noise.New)
	_ = securityNoise

	listenAddrs := libp2p.ListenAddrStrings(
		"/ip4/0.0.0.0/tcp/6666",
		"/ip4/0.0.0.0/tcp/6666/ws",
	)

	var currentDHT *dht.IpfsDHT
	newDHT := func(h host.Host) (routing.PeerRouting, error) {
		var err error

		currentDHT, err = dht.New(ctx, h, dht.V1ProtocolOverride(dhtProtocol))
		// currentDHT, err = dht.New(ctx, h, dht.ProtocolExtension(dhtProtocol))
		// currentDHT, err = dht.New(ctx, h, dht.ProtocolPrefix(dhtProtocol))
		// currentDHT, err = dht.New(ctx, h)
		return currentDHT, err
	}
	routing := libp2p.Routing(newDHT)

	host, err := libp2p.New(
		transports,
		listenAddrs,
		muxers,
		securityNoise,
		routing,
	)
	if err != nil {
		return err
	}

	// TODO: Replace our stream handler with a pubsub instance, and a handler
	// to field incoming messages on our topic.
	host.SetStreamHandler(dhtProtocol, msgHandler)

	for _, addr := range host.Addrs() {
		fmt.Println("Listening on", addr)
	}
	// targetAddr, err := ma.NewMultiaddr("/ip4/127.0.0.1/tcp/63785/p2p/QmWjz6xb8v9K4KnYEwP5Yk75k5mMBCehzWFLCvvQpYxF3d")
	targetAddr, err := ma.NewMultiaddr("/ip4/10.10.65.175/tcp/37000/p2p/12D3KooWLsR8QYxaZN3c1CrMxHCtMDQtYkNpbJrYBHDigMfqLx3x")
	if err != nil {
		return err
	}

	bootstrapPeer, err := peer.AddrInfoFromP2pAddr(targetAddr)
	if err != nil {
		log.Error().Err(err).Interface("bootstrapMaddr", targetAddr).Msg("Could not parse create peer from bootstrap address")
		return err
	}

	targetInfo, err := peer.AddrInfoFromP2pAddr(targetAddr)
	if err != nil {
		return err
	}

	err = host.Connect(ctx, *targetInfo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connecting to bootstrap: %s", err)
	} else {
		fmt.Println("Connected to", targetInfo.ID)
	}

	// notifee := &discoveryNotifee{h: host, ctx: ctx}
	// mdns := mdns.NewMdnsService(host, "", notifee)
	// if err := mdns.Start(); err != nil {
	// 	panic(err)
	// }

	err = currentDHT.Bootstrap(ctx)
	if err != nil {
		return err
	}

	routingDiscovery := drouting.NewRoutingDiscovery(currentDHT)
	dutil.Advertise(ctx, routingDiscovery, dhtProtocol)
	peers, err := dutil.FindPeers(ctx, routingDiscovery, dhtProtocol)
	if err != nil {
		return err
	}
	for _, peer := range peers {
		log.Info().Interface("peer", peer).Msg("Need to handle peer found")
		// notifee.HandlePeerFound(peer)
	}

	donec := make(chan struct{}, 1)
	// go inputLoop(ctx, host, donec)

	for {

		err = currentDHT.Ping(ctx, bootstrapPeer.ID)
		if err != nil {
			log.Error().Err(err).Str("peer", bootstrapPeer.ID.String()).Msg("Could not ping bootstrap")
			time.Sleep(20 * time.Second)
			continue
		}
		log.Info().Msg("Pinged a peer successfully")

		currentDHT.RefreshRoutingTable()
		rt := currentDHT.RoutingTable()
		rt.Print()

		hashedData := sha1.Sum([]byte("hilliard"))
		mulHash, err := mh.EncodeName(hashedData[:], "sha1")
		if err != nil {
			log.Error().Err(err).Bytes("hash", hashedData[:]).Msg("Could not encode multihash")
			break
		}

		_, err = currentDHT.GetClosestPeers(ctx, "/pk/"+string(mulHash))
		if err != nil {
			log.Error().Err(err).Msg("Could not get closest peers")
		}

		_, err = currentDHT.SearchValue(ctx, "/pk/"+string(mulHash))
		if err != nil {
			log.Error().Err(err).Msg("Could not search value")
		}
		_, err = currentDHT.GetValue(ctx, "/pk/"+string(mulHash))
		if err != nil {
			log.Error().Err(err).Msg("Could not get value")
		}
		err = currentDHT.PutValue(ctx, "/pk/"+string(mulHash), []byte{0xDE, 0xAD, 0xBE, 0xEF})
		if err != nil {
			log.Error().Err(err).Msg("Could not put value")
		}
		// log.Info().Str("routingtable", rt).Msg("Current Routing Table")
		time.Sleep(20 * time.Second)

	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT)

	select {
	case <-stop:
		host.Close()
		os.Exit(0)
	case <-donec:
		host.Close()
	}
	return nil
}

func msgHandler(s network.Stream) {
	data, err := io.ReadAll(s)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	fmt.Println("Received:", string(data))
}

func inputLoop(ctx context.Context, h host.Host, donec chan struct{}) {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		msg := scanner.Text()
		log.Info().Str("msg", msg).Msg("Received")
		for _, peer := range h.Network().Peers() {
			if _, err := h.Peerstore().SupportsProtocols(peer, string(dhtProtocol)); err == nil {
				s, err := h.NewStream(ctx, peer, dhtProtocol)
				_ = s
				defer func() {
					if err != nil {
						fmt.Fprintln(os.Stderr, err)
					}
				}()
				if err != nil {
					continue
				}
				// err = chatSend(msg, s)
			}
		}
	}
	donec <- struct{}{}
}
func init() {
	rootCmd.AddCommand(dhtCmd)

	// Here you will define your flags and configuration settings.

	// Cobra supports Persistent Flags which will work for this command
	// and all subcommands, e.g.:
	// dhtCmd.PersistentFlags().String("foo", "", "A help for foo")

	// Cobra supports local flags which will only run when this command
	// is called directly, e.g.:
	// dhtCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")
}
