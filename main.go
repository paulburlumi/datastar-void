package main

import (
	"context"
	"embed"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/sessions"
	datastar "github.com/starfederation/datastar/sdk/go"
	"golang.org/x/sync/errgroup"
)

//go:embed static/*
var staticFS embed.FS

func main() {

	ctx := context.Background()

	if err := run(ctx); err != nil {
		slog.Error("datastar-void failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	sctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	eg, ctx := errgroup.WithContext(sctx)

	eg.Go(func() error {
		router := http.NewServeMux()

		if err := setupRoutes(router); err != nil {
			return fmt.Errorf("error setting up routes: %w", err)
		}

		srv := &http.Server{
			Addr:    "0.0.0.0:9090",
			Handler: router,
		}

		go func() {
			<-ctx.Done()
			if err := srv.Shutdown(context.Background()); err != nil {
				slog.Error("error during shutdown", "error", err)
			}
		}()

		slog.Info("starting server", "addr", "http://"+srv.Addr)

		return srv.ListenAndServe()
	})

	if err := eg.Wait(); err != nil {
		return err
	}

	return nil
}

func setupRoutes(router *http.ServeMux) error {
	payloadMap := map[uuid.UUID]Payload{}
	mu := &sync.Mutex{}

	sessionStore := sessions.NewCookieStore([]byte("session-secret"))
	sessionStore.MaxAge(int(24 * time.Hour / time.Second))
	sessionStore.Options = &sessions.Options{
		Secure: false,
	}

	router.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(index())
	})

	router.Handle("/static/", http.FileServer(http.FS(staticFS)))

	router.HandleFunc("/void", func(w http.ResponseWriter, r *http.Request) {
		slog.Info("void endpoint hit")

		sse := datastar.NewSSE(w, r)

		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-r.Context().Done():
				slog.Info("context done")
				return
			case <-ticker.C:
				func() {
					mu.Lock()
					defer mu.Unlock()

					fragment := `<div id="messages">`
					for key, payload := range payloadMap {
						if time.Since(payload.Created).Seconds() > 10 {
							delete(payloadMap, key)
							continue
						}
						// slog.Info("entry", "key", key, "message", payload.Message, "created", payload.Created)
						opacity := 1 - (time.Since(payload.Created).Seconds() / 10)
						fragment += fmt.Sprintf(`<div
	id="%s"
	class="message"
	style="
		opacity:%v;
		top:%v%%;
		left:%v%%;
		background:%s;
	"
	>
	%s
	</div>`, key, opacity, payload.Y, payload.X, payload.Colour, payload.Message)
					}
					fragment += `</div>`
					if err := sse.MergeFragments(fragment); err != nil {
						slog.Error("error merging fragment", "error", err)
					}
				}()
			}
		}
	})

	router.HandleFunc("/message", func(w http.ResponseWriter, r *http.Request) {
		sessionID, err := upsertSessionID(sessionStore, r, w)
		if err != nil {
			slog.Error("error getting session ID", "error", err)
			http.Error(w, "Error getting session ID", http.StatusInternalServerError)
			return
		}
		err = r.ParseMultipartForm(1024 * 1024)
		if err != nil {
			slog.Error("error parsing form", "error", err)
			http.Error(w, "Error parsing form", http.StatusBadRequest)
			return
		}
		msgs := r.MultipartForm.Value["message"]
		if len(msgs) == 0 || msgs[0] == "" {
			slog.Error("no message provided")
			http.Error(w, "No message", http.StatusBadRequest)
			return
		}
		payload := Payload{
			Created: time.Now(),
			Message: msgs[0],
			Colour:  fmt.Sprintf("#%06x", sessionID),
			X:       math.Round((10+rand.Float64()*80)*100) / 100,
			Y:       math.Round((5+rand.Float64()*80)*100) / 100,
		}

		mu.Lock()
		defer mu.Unlock()

		payloadMap[uuid.New()] = payload
		slog.Info("message saved", "sessionID", sessionID, "message", msgs[0], "colour", payload.Colour, "x", payload.X, "y", payload.Y)
	})

	return nil
}

func upsertSessionID(store sessions.Store, r *http.Request, w http.ResponseWriter) (uint32, error) {
	sess, err := store.Get(r, "connections")
	if err != nil {
		return 0, fmt.Errorf("failed to get session: %w", err)
	}

	id, ok := sess.Values["id"].(uint32)

	if !ok {
		id = rand.Uint32N(0xFFFFFF)
		sess.Values["id"] = id
		if err := sess.Save(r, w); err != nil {
			return 0, fmt.Errorf("failed to save session: %w", err)
		}
	}

	return id, nil
}

type Payload struct {
	Created time.Time
	Message string
	Colour  string
	X       float64
	Y       float64
}

func index() []byte {
	return []byte(`<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8"/>
  <meta name="viewport" content="width=device-width, initial-scale=1.0"/>
  <title>VOID*</title>
  <link rel="stylesheet" href="/static/css/main.css"/>
  <script type="module"
    src="https://cdn.jsdelivr.net/gh/starfederation/datastar@1.0.0-beta.11/bundles/datastar.js">
  </script>
  <link href="https://fonts.googleapis.com/icon?family=Material+Icons" rel="stylesheet">
</head>
<body>
  <main>
    <div id="messages" data-on-load="@get('/void')">
    </div>
    <form data-on-submit="@post('/message', {contentType: 'form'})">
      <input name="message" placeholder="Scream into the void..."/>
    </form>
  </main>
</body>
</html>`)
}
