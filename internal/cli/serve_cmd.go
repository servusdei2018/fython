package cli

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/alecthomas/kong"
	"github.com/servusdei2018/fython/pkg/server"
)

// ServeCLI defines the command-line grammar for `fython serve`.
type ServeCLI struct {
	Target    string `arg:"" placeholder:"<module:app>" help:"Application entrypoint in module:app or path/to/app.py:app format."`
	Host      string `name:"host" default:"127.0.0.1" help:"Bind host address."`
	Port      int    `name:"port" default:"8000" help:"Bind port number."`
	Workers   int    `name:"workers" default:"0" help:"Number of worker VMs (0 for GOMAXPROCS)."`
	Interface string `name:"interface" default:"auto" enum:"auto,asgi,wsgi" help:"Web interface protocol (auto, asgi, wsgi)."`
}

// RunServe parses arguments and launches the multi-core native HTTP server.
func RunServe(args []string) error {
	var s ServeCLI
	parser, err := kong.New(
		&s,
		kong.Name("fython serve"),
		kong.Description("Run a production-grade multi-core ASGI 3.0 or PEP 3333 WSGI HTTP server."),
	)
	if err != nil {
		return err
	}
	_, err = parser.Parse(args)
	if err != nil {
		return err
	}

	workers := s.Workers
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}

	srv, err := server.NewServer(s.Target, server.Config{
		Host:      s.Host,
		Port:      s.Port,
		Workers:   workers,
		Interface: s.Interface,
	})
	if err != nil {
		return fmt.Errorf("serve failed to initialize: %w", err)
	}

	addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("serve failed to bind %s: %w", addr, err)
	}

	ifaceName := "WSGI"
	if srv.IsASGI() {
		ifaceName = "ASGI 3.0"
	}
	fmt.Fprintf(os.Stderr, "Fython 3.15.0 server started on http://%s (%s, workers: %d)\n", ln.Addr().String(), ifaceName, workers)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- srv.Serve(ln)
	}()

	select {
	case <-ctx.Done():
		fmt.Fprintf(os.Stderr, "\nReceived shutdown signal, terminating server gracefully...\n")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-serverErr:
		if err != nil && err != net.ErrClosed {
			return err
		}
		return nil
	}
}
