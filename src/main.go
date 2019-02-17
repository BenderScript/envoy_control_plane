package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"

	"sync"
	"sync/atomic"
	"time"

	"github.com/envoyproxy/go-control-plane/pkg/cache"
	xds "github.com/envoyproxy/go-control-plane/pkg/server"

	"google.golang.org/grpc"

	"github.com/envoyproxy/go-control-plane/envoy/api/v2"
	"github.com/envoyproxy/go-control-plane/envoy/api/v2/auth"
	"github.com/envoyproxy/go-control-plane/envoy/api/v2/core"
	"github.com/envoyproxy/go-control-plane/envoy/api/v2/listener"
	"github.com/envoyproxy/go-control-plane/envoy/api/v2/route"
	discovery "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v2"

	hcm "github.com/envoyproxy/go-control-plane/envoy/config/filter/network/http_connection_manager/v2"
	"github.com/envoyproxy/go-control-plane/pkg/util"
)

var (
	debug       bool
	onlyLogging bool

	localhost = "127.0.0.1"

	port        uint
	gatewayPort uint
	alsPort     uint

	mode string

	version int32

	config cache.SnapshotCache
	Info   *log.Logger
	Error  *log.Logger
	Fatal  *log.Logger
)

const (
	XdsCluster = "xds_cluster"
	Ads        = "ads"
	Xds        = "xds"
	Rest       = "rest"
)

func init() {
	Info = log.New(os.Stdout,
		"INFO: ",
		log.LstdFlags)
	Error = log.New(os.Stdout,
		"ERROR: ",
		log.LstdFlags)
	Fatal = log.New(os.Stdout,
		"FATAL: ",
		log.LstdFlags)
	flag.BoolVar(&debug, "debug", true, "Use debug logging")
	flag.BoolVar(&onlyLogging, "onlyLogging", false, "Only demo AccessLogging Service")
	flag.UintVar(&port, "port", 18000, "Management server port")
	flag.UintVar(&gatewayPort, "gateway", 19001, "Management server port for HTTP gateway")
	flag.StringVar(&mode, "ads", Ads, "Management server type (ads, xds, rest)")
}

type logger struct{}

func (logger logger) Infof(format string, args ...interface{}) {
	Info.Printf(format, args...)
}
func (logger logger) Errorf(format string, args ...interface{}) {
	Info.Printf(format, args...)
}

func (cb *callbacks) Report() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	Info.Printf("fetches: %d, requests: %d", cb.fetches, cb.requests)
}
func (cb *callbacks) OnStreamOpen(_ context.Context, id int64, typ string) error {
	Info.Printf("OnStreamOpen %d open for %s", id, typ)
	return nil
}
func (cb *callbacks) OnStreamClosed(id int64) {
	Info.Printf("OnStreamClosed %d closed", id)
}
func (cb *callbacks) OnStreamRequest(int64, *v2.DiscoveryRequest) error {
	Info.Println("OnStreamRequest")
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.requests++
	if cb.signal != nil {
		close(cb.signal)
		cb.signal = nil
	}
	return nil
}
func (cb *callbacks) OnStreamResponse(int64, *v2.DiscoveryRequest, *v2.DiscoveryResponse) {
	Info.Println("OnStreamResponse...")
	cb.Report()
}
func (cb *callbacks) OnFetchRequest(_ context.Context, req *v2.DiscoveryRequest) error {
	Info.Println("OnFetchRequest...")
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.fetches++
	if cb.signal != nil {
		close(cb.signal)
		cb.signal = nil
	}
	return nil
}

func (cb *callbacks) OnFetchResponse(*v2.DiscoveryRequest, *v2.DiscoveryResponse) {}

type callbacks struct {
	signal        chan struct{}
	fetches       int
	requests      int
	mu            sync.Mutex
	callbackError bool
}

// Hasher returns node ID as an ID
type Hasher struct {
}

// ID function
func (h Hasher) ID(node *core.Node) string {
	if node == nil {
		return "unknown"
	}
	return node.Id
}

const grpcMaxConcurrentStreams = 1000000

// RunManagementServer starts an xDS server at the given port.
func RunManagementServer(ctx context.Context, server xds.Server, port uint) {
	var grpcOptions []grpc.ServerOption
	grpcOptions = append(grpcOptions, grpc.MaxConcurrentStreams(grpcMaxConcurrentStreams))
	grpcServer := grpc.NewServer(grpcOptions...)

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		Fatal.Println("failed to listen")
	}

	// register services
	discovery.RegisterAggregatedDiscoveryServiceServer(grpcServer, server)
	v2.RegisterEndpointDiscoveryServiceServer(grpcServer, server)
	v2.RegisterClusterDiscoveryServiceServer(grpcServer, server)
	v2.RegisterRouteDiscoveryServiceServer(grpcServer, server)
	v2.RegisterListenerDiscoveryServiceServer(grpcServer, server)

	Info.Printf("management server listening on port %d", port)
	go func() {
		if err = grpcServer.Serve(lis); err != nil {
			Error.Println(err)
		}
	}()
	<-ctx.Done()

	grpcServer.GracefulStop()
}

// RunManagementGateway starts an HTTP gateway to an xDS server.
func RunManagementGateway(ctx context.Context, srv xds.Server, port uint) {
	Info.Printf("starting HTTP/1.1 gateway  on port %d", port)
	server := &http.Server{Addr: fmt.Sprintf(":%d", port), Handler: &xds.HTTPGateway{Server: srv}}
	go func() {
		if err := server.ListenAndServe(); err != nil {
			Fatal.Fatalln(err)
		}
	}()

	<-ctx.Done()
	if err := server.Shutdown(ctx); err != nil {
		Error.Fatalln(err)
	}
}

func main() {
	flag.Parse()
	ctx := context.Background()

	Info.Println("Starting control plane")

	signal := make(chan struct{})
	cb := &callbacks{
		signal:   signal,
		fetches:  0,
		requests: 0,
	}
	config = cache.NewSnapshotCache(mode == Ads, Hasher{}, logger{})

	srv := xds.NewServer(config, cb)

	if onlyLogging {
		cc := make(chan struct{})
		<-cc
		os.Exit(0)
	}

	// start the xDS server
	go RunManagementServer(ctx, srv, port)
	go RunManagementGateway(ctx, srv, gatewayPort)

	Info.Println("waiting for the first request...")
	<-signal

	cb.Report()

	for {
		atomic.AddInt32(&version, 1)
		nodeId := config.GetStatusKeys()[1]

		var clusterName = "service_bbc"
		var remoteHost = "www.bbc.com"
		var sni = "www.bbc.com"
		Info.Println(">>>>>>>>>>>>>>>>>>> creating cluster " + clusterName)

		//c := []cache.Resource{resource.MakeCluster(resource.Ads, clusterName)}

		h := &core.Address{Address: &core.Address_SocketAddress{
			SocketAddress: &core.SocketAddress{
				Address:  remoteHost,
				Protocol: core.TCP,
				PortSpecifier: &core.SocketAddress_PortValue{
					PortValue: uint32(443),
				},
			},
		}}

		c := []cache.Resource{
			&v2.Cluster{
				Name:            clusterName,
				ConnectTimeout:  2 * time.Second,
				Type:            v2.Cluster_LOGICAL_DNS,
				DnsLookupFamily: v2.Cluster_V4_ONLY,
				LbPolicy:        v2.Cluster_ROUND_ROBIN,
				Hosts:           []*core.Address{h},
				TlsContext: &auth.UpstreamTlsContext{
					Sni: sni,
				},
			},
		}

		// =================================================================================
		var listenerName = "listener_0"
		var targetHost = "www.bbc.com"
		var targetRegex = "/*"
		var virtualHostName = "local_service"
		var routeConfigName = "local_route"

		Info.Println(">>>>>>>>>>>>>>>>>>> creating listener " + listenerName)

		v := route.VirtualHost{
			Name:    virtualHostName,
			Domains: []string{"*"},

			Routes: []route.Route{{
				Match: route.RouteMatch{
					PathSpecifier: &route.RouteMatch_Regex{
						Regex: targetRegex,
					},
				},
				Action: &route.Route_Route{
					Route: &route.RouteAction{
						HostRewriteSpecifier: &route.RouteAction_HostRewrite{
							HostRewrite: targetHost,
						},
						ClusterSpecifier: &route.RouteAction_Cluster{
							Cluster: clusterName,
						},
						PrefixRewrite: "/robots.txt",
					},
				},
			}}}

		manager := &hcm.HttpConnectionManager{
			CodecType:  hcm.AUTO,
			StatPrefix: "ingress_http",
			RouteSpecifier: &hcm.HttpConnectionManager_RouteConfig{
				RouteConfig: &v2.RouteConfiguration{
					Name:         routeConfigName,
					VirtualHosts: []route.VirtualHost{v},
				},
			},
			HttpFilters: []*hcm.HttpFilter{{
				Name: util.Router,
			}},
		}
		pbst, err := util.MessageToStruct(manager)
		if err != nil {
			panic(err)
		}

		var l = []cache.Resource{
			&v2.Listener{
				Name: listenerName,
				Address: core.Address{
					Address: &core.Address_SocketAddress{
						SocketAddress: &core.SocketAddress{
							Protocol: core.TCP,
							Address:  localhost,
							PortSpecifier: &core.SocketAddress_PortValue{
								PortValue: 10000,
							},
						},
					},
				},
				FilterChains: []listener.FilterChain{{
					Filters: []listener.Filter{{
						Name:       util.HTTPConnectionManager,
						ConfigType: &listener.Filter_Config{pbst},
					}},
				}},
			}}

		// =================================================================================

		Info.Println(">>>>>>>>>>>>>>>>>>> creating snapshot Version " + fmt.Sprint(version))
		snap := cache.NewSnapshot(fmt.Sprint(version), nil, c, nil, l)

		config.SetSnapshot(nodeId, snap)

		reader := bufio.NewReader(os.Stdin)
		_, _ = reader.ReadString('\n')

	}

}
