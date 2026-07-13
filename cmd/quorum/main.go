package main

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
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

	// Services
	emailSvc := service.NewEmailService(cfg, authRepo)
	duesSvc := service.NewDuesService(duesRepo, emailSvc)
	duesSvc.StartScheduler(ctx)

	// Middleware
	mw := handler.NewMiddleware(cfg.JWTSecret)

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
	webhooksH := handler.NewWebhooksHandler(duesRepo, cfg.StripeWebhookSecret, cfg.PayPalWebhookID)

	r := chi.NewRouter()
	r.Use(chimiddleware.RealIP)
	r.Use(chimiddleware.Logger)
	r.Use(chimiddleware.Recoverer)
	r.Use(handler.SecurityHeaders)
	r.Use(handler.MaxRequestBody)

	r.Route("/api/v1", func(r chi.Router) {
		r.Post("/auth/bootstrap", authH.Bootstrap)
		r.With(mw.LoginRateLimit).Post("/auth/login", authH.Login)
		r.With(mw.LoginRateLimit).Post("/auth/refresh", authH.Refresh)

		// Webhooks are unauthenticated but signature-verified.
		r.Post("/webhooks/stripe", webhooksH.Stripe)
		r.Post("/webhooks/paypal", webhooksH.PayPal)

		r.Group(func(r chi.Router) {
			r.Use(mw.Auth)
			r.Use(handler.AuditMiddleware(auditRepo))

			r.Post("/auth/logout", authH.Logout)
			r.Get("/auth/me", authH.Me)
			r.Patch("/auth/me/password", authH.ChangePassword)

			r.With(mw.RequireRole("admin")).Get("/users", authH.ListUsers)
			r.With(mw.RequireRole("admin")).Post("/users", authH.CreateUser)
			r.With(mw.RequireRole("admin")).Patch("/users/{id}", authH.UpdateUserRole)
			r.With(mw.RequireRole("admin")).Delete("/users/{id}", authH.DeleteUser)

			r.Get("/dashboard", dashH.Summary)

			r.Get("/members", membersH.List)
			r.With(mw.RequireRole("officer")).Post("/members", membersH.Create)
			r.Get("/members/{id}", membersH.Get)
			r.With(mw.RequireRole("officer")).Patch("/members/{id}", membersH.Update)
			r.With(mw.RequireRole("admin")).Delete("/members/{id}", membersH.Delete)
			r.Get("/members/{id}/dues", membersH.GetDues)
			r.Get("/members/{id}/action-items", membersH.GetActionItems)

			r.With(mw.RequireRole("officer")).Get("/dues", duesH.List)
			r.With(mw.RequireRole("officer")).Post("/dues", duesH.Create)
			r.With(mw.RequireRole("officer")).Get("/dues/{id}", duesH.Get)
			r.With(mw.RequireRole("officer")).Patch("/dues/{id}", duesH.Update)
			r.With(mw.RequireRole("officer")).Post("/dues/{id}/transactions", duesH.CreateTransaction)
			r.With(mw.RequireRole("officer")).Get("/dues/transactions", duesH.ListTransactions)

			r.Get("/meetings", meetingsH.List)
			r.With(mw.RequireRole("officer")).Post("/meetings", meetingsH.Create)
			r.Get("/meetings/{id}", meetingsH.Get)
			r.With(mw.RequireRole("officer")).Patch("/meetings/{id}", meetingsH.Update)
			r.With(mw.RequireRole("officer")).Delete("/meetings/{id}", meetingsH.Delete)
			r.With(mw.RequireRole("officer")).Put("/meetings/{id}/attendees", meetingsH.SetAttendees)
			r.With(mw.RequireRole("officer")).Post("/meetings/{id}/decisions", meetingsH.CreateDecision)
			r.With(mw.RequireRole("officer")).Patch("/meetings/{id}/decisions/{did}", meetingsH.UpdateDecision)
			r.With(mw.RequireRole("officer")).Delete("/meetings/{id}/decisions/{did}", meetingsH.DeleteDecision)

			r.Get("/action-items", actionItemsH.List)
			r.Post("/action-items", actionItemsH.Create)
			r.Patch("/action-items/{id}", actionItemsH.Update)
			r.With(mw.RequireRole("officer")).Delete("/action-items/{id}", actionItemsH.Delete)

			r.Get("/plans", plansH.List)
			r.With(mw.RequireRole("officer")).Post("/plans", plansH.Create)
			r.Get("/plans/{id}", plansH.Get)
			r.With(mw.RequireRole("officer")).Patch("/plans/{id}", plansH.Update)
			r.With(mw.RequireRole("officer")).Delete("/plans/{id}", plansH.Delete)
			r.With(mw.RequireRole("officer")).Post("/plans/{id}/decisions", plansH.CreateDecision)
			r.With(mw.RequireRole("officer")).Patch("/plans/{id}/decisions/{did}", plansH.UpdateDecision)
			r.With(mw.RequireRole("officer")).Delete("/plans/{id}/decisions/{did}", plansH.DeleteDecision)

			r.Get("/contacts", contactsH.List)
			r.With(mw.RequireRole("officer")).Post("/contacts", contactsH.Create)
			r.Get("/contacts/{id}", contactsH.Get)
			r.With(mw.RequireRole("officer")).Patch("/contacts/{id}", contactsH.Update)
			r.With(mw.RequireRole("officer")).Delete("/contacts/{id}", contactsH.Delete)

			r.Get("/resources", resourcesH.List)
			r.With(mw.RequireRole("officer")).Post("/resources", resourcesH.Create)
			r.Get("/resources/{id}", resourcesH.Get)
			r.With(mw.RequireRole("officer")).Patch("/resources/{id}", resourcesH.Update)
			r.With(mw.RequireRole("officer")).Delete("/resources/{id}", resourcesH.Delete)
		})
	})

	// Static file serving. Hash-based routing means the browser always requests /
	// for page navigation, so no SPA fallback is needed beyond serving index.html at /.
	webFS, err := fs.Sub(quorum.WebFS, "web")
	if err != nil {
		log.Fatalf("embed sub: %v", err)
	}
	r.Handle("/*", http.FileServer(http.FS(webFS)))

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
	log.Println("done")
}
