package server

import (
	"fmt"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"micro/go-log"
	"micro/go-micro/codec"
	"micro/go-micro/metadata"
	"micro/go-micro/registry"
	"micro/go-micro/transport"

	"micro/misc/lib/addr"

	"golang.org/x/net/context"
)

type rpcServer struct {
	rpc  *server
	exit chan chan error

	sync.RWMutex
	opts        Options
	handlers    map[string]Handler
	// used for first registration
	registered bool
	// graceful exit
	wg sync.WaitGroup
}

func newRpcServer(opts ...Option) Server {
	options := newOptions(opts...)
	return &rpcServer{
		opts: options,
		rpc: &server{
			name:         options.Name,
			serviceMap:   make(map[string]*service),
			hdlrWrappers: options.HdlrWrappers,
		},
		handlers:    make(map[string]Handler),
		exit:        make(chan chan error),
	}
}

func (s *rpcServer) accept(sock transport.Socket) {
	defer func() {
		// close socket
		sock.Close()

		if r := recover(); r != nil {
			log.Log("panic recovered: ", r)
			log.Log(string(debug.Stack()))
		}
	}()

	for {
		var msg transport.Message
		if err := sock.Recv(&msg); err != nil {
			return
		}

		// we use this Timeout header to set a server deadline
		to := msg.Header["Timeout"]
		// we use this Content-Type header to identify the codec needed
		ct := msg.Header["Content-Type"]

		cf, err := s.newCodec(ct)
		// TODO: needs better error handling
		if err != nil {
			sock.Send(&transport.Message{
				Header: map[string]string{
					"Content-Type": "text/plain",
				},
				Body: []byte(err.Error()),
			})
			return
		}

		codec := newRpcPlusCodec(&msg, sock, cf)

		// strip our headers
		hdr := make(map[string]string)
		for k, v := range msg.Header {
			hdr[k] = v
		}
		delete(hdr, "Content-Type")
		delete(hdr, "Timeout")

		ctx := metadata.NewContext(context.Background(), hdr)

		// set the timeout if we have it
		if len(to) > 0 {
			if n, err := strconv.ParseUint(to, 10, 64); err == nil {
				ctx, _ = context.WithTimeout(ctx, time.Duration(n))
			}
		}

		// add to wait group
		s.wg.Add(1)
		defer s.wg.Done()

		// TODO: needs better error handling
		if err := s.rpc.serveRequest(ctx, codec, ct); err != nil {
			log.Logf("Unexpected error serving request, closing socket: %v", err)
			return
		}
	}
}

func (s *rpcServer) newCodec(contentType string) (codec.NewCodec, error) {
	if cf, ok := s.opts.Codecs[contentType]; ok {
		return cf, nil
	}
	if cf, ok := defaultCodecs[contentType]; ok {
		return cf, nil
	}
	return nil, fmt.Errorf("Unsupported Content-Type: %s", contentType)
}

func (s *rpcServer) Options() Options {
	s.RLock()
	opts := s.opts
	s.RUnlock()
	return opts
}

func (s *rpcServer) Init(opts ...Option) error {
	s.Lock()
	for _, opt := range opts {
		opt(&s.opts)
	}
	// update internal server
	s.rpc = &server{
		name:         s.opts.Name,
		serviceMap:   s.rpc.serviceMap,
		hdlrWrappers: s.opts.HdlrWrappers,
	}
	s.Unlock()
	return nil
}

func (s *rpcServer) NewHandler(h interface{}, opts ...HandlerOption) Handler {
	return newRpcHandler(h, opts...)
}

func (s *rpcServer) Handle(h Handler) error {
	s.Lock()
	defer s.Unlock()

	if err := s.rpc.register(h.Handler()); err != nil {
		return err
	}

	s.handlers[h.Name()] = h

	return nil
}

func (s *rpcServer) Register() error {
	// parse address for host, port
	config := s.Options()
	var advt, host string
	var port int

	// check the advertise address first
	// if it exists then use it, otherwise
	// use the address
	if len(config.Advertise) > 0 {
		advt = config.Advertise
	} else {
		advt = config.Address
	}

	parts := strings.Split(advt, ":")
	if len(parts) > 1 {
		host = strings.Join(parts[:len(parts)-1], ":")
		port, _ = strconv.Atoi(parts[len(parts)-1])
	} else {
		host = parts[0]
	}

	addr, err := addr.Extract(host)
	if err != nil {
		return err
	}

	// register service
	node := &registry.Node{
		Id:       config.Name + "-" + config.Id,
		Address:  addr,
		Port:     port,
		Metadata: config.Metadata,
	}

	node.Metadata["transport"] = config.Transport.String()
	node.Metadata["server"] = s.String()
	node.Metadata["registry"] = config.Registry.String()

	s.RLock()
	// Maps are ordered randomly, sort the keys for consistency
	var handlerList []string
	for n, e := range s.handlers {
		// Only advertise non internal handlers
		if !e.Options().Internal {
			handlerList = append(handlerList, n)
		}
	}
	sort.Strings(handlerList)

	var endpoints []*registry.Endpoint
	for _, n := range handlerList {
		endpoints = append(endpoints, s.handlers[n].Endpoints()...)
	}
	s.RUnlock()

	service := &registry.Service{
		Name:      config.Name,
		Version:   config.Version,
		Nodes:     []*registry.Node{node},
		Endpoints: endpoints,
	}

	s.Lock()
	registered := s.registered
	s.Unlock()

	if !registered {
		log.Logf("Registering node: %s", node.Id)
	}

	// create registry options
	rOpts := []registry.RegisterOption{registry.RegisterTTL(config.RegisterTTL)}

	if err := config.Registry.Register(service, rOpts...); err != nil {
		return err
	}

	// already registered? don't need to register subscribers
	if registered {
		return nil
	}

	s.Lock()
	defer s.Unlock()

	s.registered = true
	return nil
}

func (s *rpcServer) Deregister() error {
	config := s.Options()
	var advt, host string
	var port int

	// check the advertise address first
	// if it exists then use it, otherwise
	// use the address
	if len(config.Advertise) > 0 {
		advt = config.Advertise
	} else {
		advt = config.Address
	}

	parts := strings.Split(advt, ":")
	if len(parts) > 1 {
		host = strings.Join(parts[:len(parts)-1], ":")
		port, _ = strconv.Atoi(parts[len(parts)-1])
	} else {
		host = parts[0]
	}

	addr, err := addr.Extract(host)
	if err != nil {
		return err
	}

	node := &registry.Node{
		Id:      config.Name + "-" + config.Id,
		Address: addr,
		Port:    port,
	}

	service := &registry.Service{
		Name:    config.Name,
		Version: config.Version,
		Nodes:   []*registry.Node{node},
	}

	log.Logf("Deregistering node: %s", node.Id)
	if err := config.Registry.Deregister(service); err != nil {
		return err
	}

	s.Lock()

	if !s.registered {
		s.Unlock()
		return nil
	}

	s.registered = false
	s.Unlock()
	return nil
}

func (s *rpcServer) Start() error {
	registerDebugHandler(s)
	config := s.Options()

	ts, err := config.Transport.Listen(config.Address)
	if err != nil {
		return err
	}

	log.Logf("Listening on %s", ts.Addr())
	s.Lock()
	s.opts.Address = ts.Addr()
	s.Unlock()

	go ts.Accept(s.accept)

	go func() {
		// wait for exit
		ch := <-s.exit

		// wait for requests to finish
		if wait(s.opts.Context) {
			s.wg.Wait()
		}

		// close transport listener
		ch <- ts.Close()
	}()

	return nil
}

func (s *rpcServer) Stop() error {
	ch := make(chan error)
	s.exit <- ch
	return <-ch
}

func (s *rpcServer) String() string {
	return "rpc"
}
