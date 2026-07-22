// Command quorum is the Quorum server entry point: it loads config, runs
// migrations, wires handlers and background jobs, and serves the API + web app.
package main

import (
	"context"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"

	quorum "quorum"
	"quorum/internal/config"
	"quorum/internal/db"
	"quorum/internal/handler"
	"quorum/internal/repo"
	"quorum/internal/service"
)

func main() {
	// -healthcheck lets the scratch container (which ships no shell or curl)
	// health-check itself: `quorum -healthcheck` GETs /healthz and exits 0/1.
	healthcheck := flag.Bool("healthcheck", false, "probe the local /healthz endpoint and exit")
	flag.Parse()
	if *healthcheck {
		os.Exit(runHealthcheck())
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db connect: %v", err)
	}
	defer pool.Close()

	if err := db.Migrate(ctx, pool); err != nil {
		log.Fatalf("db migrate: %v", err)
	}

	// Repos
	authRepo := repo.NewAuthRepo(pool)
	membersRepo := repo.NewMembersRepo(pool)
	duesRepo := repo.NewDuesRepo(pool)
	meetingsRepo := repo.NewMeetingsRepo(pool)
	plansRepo := repo.NewPlansRepo(pool)
	contactsRepo := repo.NewContactsRepo(pool)
	resourcesRepo := repo.NewResourcesRepo(pool)
	actionItemsRepo := repo.NewActionItemsRepo(pool)
	auditRepo := repo.NewAuditRepo(pool)
	maintenanceRepo := repo.NewMaintenanceRepo(pool)

	// Services
	emailSvc := service.NewEmailService(cfg, authRepo)
	duesSvc := service.NewDuesService(duesRepo, emailSvc, maintenanceRepo)
	schedDone := duesSvc.StartScheduler(ctx)

	// Middleware
	mw := handler.NewMiddleware(cfg.JWTSecret, cfg.TrustProxyHeaders)

	// Handlers
	authH := handler.NewAuthHandler(authRepo, cfg)
	dashH := handler.NewDashboardHandler(duesRepo, membersRepo, meetingsRepo, actionItemsRepo)
	membersH := handler.NewMembersHandler(membersRepo, actionItemsRepo, duesRepo)
	duesH := handler.NewDuesHandler(duesRepo)
	meetingsH := handler.NewMeetingsHandler(meetingsRepo)
	plansH := handler.NewPlansHandler(plansRepo)
	contactsH := handler.NewContactsHandler(contactsRepo)
	resourcesH := handler.NewResourcesHandler(resourcesRepo)
	actionItemsH := handler.NewActionItemsHandler(actionItemsRepo)
	webhooksH := handler.NewWebhooksHandler(duesRepo, cfg.StripeWebhookSecret, cfg.PayPalWebhookID, cfg.AllowUnsignedWebhooks)
	if cfg.AllowUnsignedWebhooks {
		log.Println("WARNING: QUORUM_ALLOW_UNSIGNED_WEBHOOKS is set — webhook signature verification is DISABLED for providers without a configured secret. Never use this in production.")
	}

	// Notify the governance body when a record is permanently deleted.
	notifier := service.NewNotifier(emailSvc, authRepo)
	authH.SetNotifier(notifier)
	meetingsH.SetNotifier(notifier)
	plansH.SetNotifier(notifier)
	contactsH.SetNotifier(notifier)
	resourcesH.SetNotifier(notifier)
	actionItemsH.SetNotifier(notifier)

	r := chi.NewRouter()
	// Note: chi's RealIP middleware is deliberately NOT used — it trusts
	// X-Forwarded-For from any client, which would let attackers rotate the
	// header to bypass the login rate limiter. Rate limiting keys on the raw
	// socket address instead.
	r.Use(chimiddleware.Logger)
	r.Use(chimiddleware.Recoverer)
	r.Use(handler.SecurityHeaders)
	r.Use(handler.MaxRequestBody)

	r.Get("/healthz", handler.Healthz)
	r.Get("/readyz", handler.Readyz(pool))

	r.Route("/api/v1", func(r chi.Router) {
		r.With(mw.LoginRateLimit).Post("/auth/bootstrap", authH.Bootstrap)
		r.With(mw.LoginRateLimit).Post("/auth/login", authH.Login)
		r.With(mw.RefreshRateLimit).Post("/auth/refresh", authH.Refresh)

		// Webhooks are unauthenticated but signature-verified.
		r.Post("/webhooks/stripe", webhooksH.Stripe)
		r.Post("/webhooks/paypal", webhooksH.PayPal)

		r.Group(func(r chi.Router) {
			r.Use(mw.Auth)
			r.Use(handler.AuditMiddleware(auditRepo))

			r.Post("/auth/logout", authH.Logout)
			r.Get("/auth/me", authH.Me)
			r.Patch("/auth/me/password", authH.ChangePassword)

			// User management is admin+; minting/altering a superadmin is gated
			// inside the handler. Deleting an account is a superadmin action.
			r.With(mw.RequireRole("admin")).Get("/users", authH.ListUsers)
			r.With(mw.RequireRole("admin")).Post("/users", authH.CreateUser)
			r.With(mw.RequireRole("admin")).Patch("/users/{id}", authH.UpdateUser)
			r.With(mw.RequireRole("superadmin")).Delete("/users/{id}", authH.DeleteUser)

			// Org-wide reads require at least `member`; `restricted` users are
			// blocked here and reach only their own record via the member-scoped
			// endpoints below (which enforce ownership in-handler).
			r.With(mw.RequireRole("member")).Get("/dashboard", dashH.Summary)

			r.With(mw.RequireRole("member")).Get("/members", membersH.List)
			r.With(mw.RequireRole("officer")).Post("/members", membersH.Create)
			r.Get("/members/{id}", membersH.Get) // ownership-scoped
			r.With(mw.RequireRole("officer")).Patch("/members/{id}", membersH.Update)
			r.With(mw.RequireRole("admin")).Delete("/members/{id}", membersH.Delete) // soft-delete
			r.Get("/members/{id}/dues", membersH.GetDues)                            // ownership-scoped
			r.Get("/members/{id}/action-items", membersH.GetActionItems)             // ownership-scoped

			r.With(mw.RequireRole("officer")).Get("/dues", duesH.List)
			r.With(mw.RequireRole("officer")).Post("/dues", duesH.Create)
			r.With(mw.RequireRole("officer")).Get("/dues/{id}", duesH.Get)
			r.With(mw.RequireRole("officer")).Patch("/dues/{id}", duesH.Update)
			r.With(mw.RequireRole("officer")).Post("/dues/{id}/transactions", duesH.CreateTransaction)
			r.With(mw.RequireRole("officer")).Get("/dues/transactions", duesH.ListTransactions)

			r.With(mw.RequireRole("member")).Get("/meetings", meetingsH.List)
			r.With(mw.RequireRole("officer")).Post("/meetings", meetingsH.Create)
			r.With(mw.RequireRole("member")).Get("/meetings/{id}", meetingsH.Get)
			r.With(mw.RequireRole("officer")).Patch("/meetings/{id}", meetingsH.Update)
			r.With(mw.RequireRole("superadmin")).Delete("/meetings/{id}", meetingsH.Delete)
			r.With(mw.RequireRole("officer")).Put("/meetings/{id}/attendees", meetingsH.SetAttendees)
			r.With(mw.RequireRole("officer")).Post("/meetings/{id}/decisions", meetingsH.CreateDecision)
			r.With(mw.RequireRole("officer")).Patch("/meetings/{id}/decisions/{did}", meetingsH.UpdateDecision)
			r.With(mw.RequireRole("officer")).Delete("/meetings/{id}/decisions/{did}", meetingsH.DeleteDecision)

			r.With(mw.RequireRole("member")).Get("/action-items", actionItemsH.List)
			r.With(mw.RequireRole("officer")).Post("/action-items", actionItemsH.Create)
			r.With(mw.RequireRole("officer")).Patch("/action-items/{id}", actionItemsH.Update)
			r.With(mw.RequireRole("superadmin")).Delete("/action-items/{id}", actionItemsH.Delete)

			r.With(mw.RequireRole("member")).Get("/plans", plansH.List)
			r.With(mw.RequireRole("officer")).Post("/plans", plansH.Create)
			r.With(mw.RequireRole("member")).Get("/plans/{id}", plansH.Get)
			r.With(mw.RequireRole("officer")).Patch("/plans/{id}", plansH.Update)
			r.With(mw.RequireRole("superadmin")).Delete("/plans/{id}", plansH.Delete)
			r.With(mw.RequireRole("officer")).Post("/plans/{id}/decisions", plansH.CreateDecision)
			r.With(mw.RequireRole("officer")).Patch("/plans/{id}/decisions/{did}", plansH.UpdateDecision)
			r.With(mw.RequireRole("officer")).Delete("/plans/{id}/decisions/{did}", plansH.DeleteDecision)

			r.With(mw.RequireRole("member")).Get("/contacts", contactsH.List)
			r.With(mw.RequireRole("officer")).Post("/contacts", contactsH.Create)
			r.With(mw.RequireRole("member")).Get("/contacts/{id}", contactsH.Get)
			r.With(mw.RequireRole("officer")).Patch("/contacts/{id}", contactsH.Update)
			r.With(mw.RequireRole("superadmin")).Delete("/contacts/{id}", contactsH.Delete)

			r.With(mw.RequireRole("member")).Get("/resources", resourcesH.List)
			r.With(mw.RequireRole("officer")).Post("/resources", resourcesH.Create)
			r.With(mw.RequireRole("member")).Get("/resources/{id}", resourcesH.Get)
			r.With(mw.RequireRole("officer")).Patch("/resources/{id}", resourcesH.Update)
			r.With(mw.RequireRole("superadmin")).Delete("/resources/{id}", resourcesH.Delete)
		})
	})

	// Static file serving. Hash-based routing means the browser always requests /
	// for page navigation, so no SPA fallback is needed beyond serving index.html at /.
	webFS, err := fs.Sub(quorum.WebFS, "web")
	if err != nil {
		log.Fatalf("embed sub: %v", err)
	}
	r.Handle("/*", staticHandler(webFS))

	addr := fmt.Sprintf(":%s", cfg.Port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		log.Printf("quorum listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("shutting down...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("shutdown: %v", err)
	}

	// Drain any in-flight deletion notices, then stop the scheduler, all before
	// the deferred pool.Close() so nothing runs against a closed pool.
	notifier.Close()
	cancel()
	select {
	case <-schedDone:
	case <-time.After(5 * time.Second):
		log.Println("scheduler did not stop in time")
	}
	log.Println("done")
}

// staticHandler serves the embedded web assets, but returns 404 for directory
// paths (no auto-generated listings) and sets a short cache lifetime so the
// same-session revisits skip re-downloading unchanged JS/CSS.
func staticHandler(webFS fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(webFS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := strings.TrimPrefix(r.URL.Path, "/")
		// Block directory listings: a trailing slash, the root, or a path that
		// resolves to a directory is not a servable asset.
		if clean == "" {
			clean = "index.html"
		}
		if strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		if f, err := webFS.Open(clean); err == nil {
			info, statErr := f.Stat()
			f.Close()
			if statErr == nil && info.IsDir() {
				http.NotFound(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "public, max-age=300")
		fileServer.ServeHTTP(w, r)
	})
}

// runHealthcheck probes the local liveness endpoint and returns a process exit
// code (0 healthy, 1 otherwise). Used by the container HEALTHCHECK since the
// scratch image has no shell or HTTP client of its own.
func runHealthcheck() int {
	port := os.Getenv("QUORUM_PORT")
	if port == "" {
		port = "8080"
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%s/healthz", port))
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "healthcheck: status %d\n", resp.StatusCode)
		return 1
	}
	return 0
}
