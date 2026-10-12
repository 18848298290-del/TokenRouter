package lifecycle

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"time"
)

// Serve runs the application or setup server until a listener failure or context cancellation.
// HTTP shutdown has a separate five-second budget; callers drain background work afterward.
func Serve(ctx context.Context, server *http.Server) error {
	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp", server.Addr)
	if err != nil {
		return err
	}
	log.Printf("Server started on %s", server.Addr)
	defer log.Println("Server exited")
	result := make(chan error, 1)
	go func() { result <- server.Serve(listener) }()
	select {
	case err = <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		log.Println("Shutting down server...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err = server.Shutdown(shutdownCtx)
		if err != nil {
			err = errors.Join(err, server.Close())
		}
		serveErr := <-result
		if !errors.Is(serveErr, http.ErrServerClosed) {
			err = errors.Join(err, serveErr)
		}
		return err
	}
}
