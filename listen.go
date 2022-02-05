package lup

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/huin/goupnp/dcps/internetgateway2"
	"golang.org/x/sync/errgroup"
)

type RouterClient interface {
	AddPortMapping(
		NewRemoteHost string,
		NewExternalPort uint16,
		NewProtocol string,
		NewInternalPort uint16,
		NewInternalClient string,
		NewEnabled bool,
		NewPortMappingDescription string,
		NewLeaseDuration uint32,
	) (err error)

	GetExternalIPAddress() (
		NewExternalIPAddress string,
		err error,
	)
}

type UpListener struct {
	ctx   context.Context
	c     RouterClient
	proto string
	port  int
	name  string

	mutex sync.Mutex

	net.Listener
}

var l net.Listener = &UpListener{}

func NewUpListener(ctx context.Context) *UpListener {
	return &UpListener{
		ctx: ctx,
	}
}

func (upl *UpListener) Name() string {
	if upl.name == "" {
		upl.name = "LUP" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return upl.name
}

func (upl *UpListener) GetInternalIPAddress() (string, error) {
	// NOTE: this does not actually connect to cloudflare, it's just a
	// local socket.
	conn, error := net.Dial("udp", "1.1.1.1:80")
	if error != nil {
		return "", error
	}

	defer conn.Close()
	ipAddress := conn.LocalAddr().(*net.UDPAddr)
	return ipAddress.String(), nil
}

func (upl *UpListener) PickRouterClient() (RouterClient, error) {
	if upl.ctx == nil {
		upl.ctx = context.Background()
	}
	tasks, _ := errgroup.WithContext(upl.ctx)
	// Request each type of client in parallel, and return what is found.
	var ip1Clients []*internetgateway2.WANIPConnection1
	tasks.Go(func() error {
		var err error
		ip1Clients, _, err = internetgateway2.NewWANIPConnection1Clients()
		return err
	})
	var ip2Clients []*internetgateway2.WANIPConnection2
	tasks.Go(func() error {
		var err error
		ip2Clients, _, err = internetgateway2.NewWANIPConnection2Clients()
		return err
	})
	var ppp1Clients []*internetgateway2.WANPPPConnection1
	tasks.Go(func() error {
		var err error
		ppp1Clients, _, err = internetgateway2.NewWANPPPConnection1Clients()
		return err
	})

	if err := tasks.Wait(); err != nil {
		return nil, err
	}

	// Trivial handling for where we find exactly one device to talk to, you
	// might want to provide more flexible handling than this if multiple
	// devices are found.
	switch {
	case len(ip2Clients) == 1:
		return ip2Clients[0], nil
	case len(ip1Clients) == 1:
		return ip1Clients[0], nil
	case len(ppp1Clients) == 1:
		return ppp1Clients[0], nil
	default:
		return nil, errors.New("multiple or no services found")
	}
}

func (upl *UpListener) GetIPAndForwardPort() error {
	var err error
	upl.c, err = upl.PickRouterClient()
	if err != nil {
		return err
	}

	externalIP, err := upl.c.GetExternalIPAddress()
	if err != nil {
		return err
	}
	fmt.Println("Our external IP address is: ", externalIP)

	internalIP, err := upl.GetInternalIPAddress()
	if err != nil {
		return err
	}
	fmt.Println("Our internal IP address is: ", internalIP)

	switch strings.ToLower(upl.proto) {
	case "tcp":
		upl.proto = "TCP"
	case "udp":
		upl.proto = "UDP"
	default:
		return errors.New("unsupported protocol")
	}

	return upl.c.AddPortMapping(
		"",
		// External port number to expose to Internet:
		uint16(upl.port),
		// Protocol to expose:
		upl.proto,
		// Internal port number on the LAN to forward to.
		// Some routers might not support this being different to the external
		// port number.
		uint16(upl.port),
		// Internal address on the LAN we want to forward to.
		internalIP,
		// Enabled:
		true,
		// Informational description for the client requesting the port forwarding.
		upl.Name(),
		// How long should the port forward last for in seconds.
		// If you want to keep it open for longer and potentially across router
		// resets, you might want to periodically request before this elapses.
		3600,
	)
}

func (upl *UpListener) UPnPLoop(cancel context.CancelFunc) {
	upl.mutex.Lock()
	defer upl.mutex.Unlock()
	defer cancel()
	for {
		log.Printf("Getting IP and forwarding port")
		err := upl.GetIPAndForwardPort()
		if err != nil {
			fmt.Println("Error: ", err)
		}
		// request a new one in 3500 seconds
		time.Sleep(time.Second * 3500)
	}
}

func (upl *UpListener) Poke(proto, addr string) error {
	var cancel context.CancelFunc
	ctx, cancel := context.WithCancel(context.Background())
	upl.ctx = ctx
	defer cancel()
	_, port, _ := net.SplitHostPort(addr)
	var err error
	upl.port, err = strconv.Atoi(port)
	if err != nil {
		fmt.Errorf("%s not a port", port)
	}
	go upl.UPnPLoop(cancel)
	return nil
}

func (upl *UpListener) Accept() (net.Conn, error) {
	return upl.Listener.Accept()
}

func (upl *UpListener) Addr() net.Addr {
	internalIP, err := upl.GetInternalIPAddress()
	if err != nil {
		return nil
	}
	if strings.HasPrefix(upl.proto, "tcp") {
		return &net.TCPAddr{
			IP:   net.ParseIP(internalIP),
			Port: upl.port,
		}
	}
	if strings.HasPrefix(upl.proto, "udp") {
		return &net.UDPAddr{
			IP:   net.ParseIP(internalIP),
			Port: upl.port,
		}
	}
	return &net.IPAddr{
		IP: net.ParseIP(internalIP),
	}
}

func (upl *UpListener) Listen(proto, addr string) (net.Listener, error) {
	err := upl.Poke(proto, addr)
	if err != nil {
		return nil, err
	}
	upl.Listener, err = net.Listen(proto, addr)
	if err != nil {
		return nil, err
	}
	return upl, err
}

func Listen(proto, addr string) (net.Listener, error) {
	return NewUpListener(context.TODO()).Listen(proto, addr)
}
