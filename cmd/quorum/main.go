// Command quorum is the Quorum server entry point: it loads config, runs
// migrations, wires handlers and background jobs, and serves the API + web app.
package main

import (
	"context"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	quorum "quorum"
	"quorum/internal/config"
	"quorum/internal/db"
	"quorum/internal/handler"
	"quorum/internal/metrics"
	"quorum/internal/repo"
	"quorum/internal/service"
)

func main() {
	// -healthcheck lets the scratch container (which ships no shell or curl)
	// health-check itself: `quorum -healthcheck` GETs /healthz and exits 0/1.
	healthcheck := flag.Bool("healthcheck", false, "probe the local /healthz endpoint and exit")
	// -migrate-down rolls the schema back to a target version and exits, for the
	// documented "roll back a bad migration" runbook path. -1 means "not asked".
	migrateDown := flag.Int("migrate-down", -1, "roll the schema back to this version, then exit (0 unwinds everything)")
	// -unlock-2fa clears TOTP + recovery codes for one account and exits: the
	// break-glass path for a sole admin who lost their authenticator.
	unlock2FA := flag.String("unlock-2fa", "", "disable two-factor for this email address, then exit (break-glass)")
	// -verify-audit recomputes the audit log's hash chain and exits 0 (intact)
	// or 1 (broken) — for cron, CI, and pre-engagement checks by auditors.
	verifyAudit := flag.Bool("verify-audit", false, "verify the audit log hash chain, then exit")
	flag.Parse()
	if *healthcheck {
		os.Exit(runHealthcheck())
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	// Structured JSON logging to stdout; the standard log package still works
	// (it's used for early fatals above), but request logs and panics go through
	// slog with a request_id for correlation.
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel(cfg.LogLevel)})))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db connect: %v", err)
	}
	defer pool.Close()

	// Operator one-shots run against the connected pool and exit before any
	// server or background worker starts.
	if *migrateDown >= 0 {
		if err := db.MigrateDown(ctx, pool, *migrateDown); err != nil {
			log.Fatalf("migrate-down: %v", err)
		}
		log.Printf("schema rolled back to version %d", *migrateDown)
		return
	}
	if err := db.Migrate(ctx, pool); err != nil {
		log.Fatalf("db migrate: %v", err)
	}

	if *verifyAudit {
		st, err := repo.NewAuditRepo(pool).VerifyChain(ctx)
		if err != nil {
			log.Fatalf("verify-audit: %v", err)
		}
		if !st.OK {
			log.Printf("AUDIT CHAIN BROKEN at seq %d (%d entries, head seq %d)", st.BrokenSeq, st.Entries, st.HeadSeq)
			os.Exit(1)
		}
		log.Printf("audit chain intact: %d entries, head seq %d, head hash %s", st.Entries, st.HeadSeq, st.HeadHash)
		return
	}

	// unlock-2fa runs after Migrate so it works on a fresh/upgraded schema.
	if *unlock2FA != "" {
		if err := runUnlock2FA(ctx, pool, *unlock2FA); err != nil {
			log.Fatalf("unlock-2fa: %v", err)
		}
		return
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
	governanceRepo := repo.NewGovernanceRepo(pool)
	fxRepo := repo.NewFXRepo(pool)
	budgetRepo := repo.NewBudgetRepo(pool, fxRepo)
	analyticsRepo := repo.NewAnalyticsRepo(pool, fxRepo)
	notifyRepo := repo.NewNotifyRepo(pool)

	// Services
	emailSvc := service.NewEmailService(cfg, authRepo)
	duesSvc := service.NewDuesService(duesRepo, emailSvc, maintenanceRepo, duesRepo)
	duesSvc.SetAuditRetention(time.Duration(cfg.AuditRetentionDays) * 24 * time.Hour)
	schedDone := duesSvc.StartScheduler(ctx)
	notifySvc := service.NewNotificationService(notifyRepo, emailSvc)

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
	governanceH := handler.NewGovernanceHandler(governanceRepo, cfg)
	budgetH := handler.NewBudgetHandler(budgetRepo)
	analyticsH := handler.NewAnalyticsHandler(analyticsRepo)
	fxH := handler.NewFXHandler(fxRepo)
	notificationsH := handler.NewNotificationsHandler(notifyRepo)
	auditH := handler.NewAuditHandler(auditRepo)
	auditH.SetVerifier(auditRepo)
	exportH := handler.NewExportHandler(membersRepo, duesRepo, authRepo)
	webhooksH := handler.NewWebhooksHandler(duesRepo, cfg.StripeWebhookSecret, cfg.PayPalWebhookID, cfg.AllowUnsignedWebhooks)
	if cfg.AllowUnsignedWebhooks {
		log.Println("WARNING: QUORUM_ALLOW_UNSIGNED_WEBHOOKS is set — webhook signature verification is DISABLED for providers without a configured secret. Never use this in production.")
	}

	// Notify the governance body when a record is permanently deleted.
	notifier := service.NewNotifier(emailSvc, authRepo)
	authH.SetNotifier(notifier)
	authH.SetMailer(emailSvc)       // password-reset links (no-ops when SMTP is unconfigured)
	authH.SetAuditLogger(auditRepo) // auth endpoints live outside AuditMiddleware
	governanceH.SetMailer(emailSvc) // async ballot links
	meetingsH.SetNotifier(notifier)
	plansH.SetNotifier(notifier)
	contactsH.SetNotifier(notifier)
	resourcesH.SetNotifier(notifier)
	actionItemsH.SetNotifier(notifier)

	// Automatic event notifications (in-app + email) for governance, meetings,
	// and assignments.
	governanceH.SetEventNotifier(notifySvc)
	meetingsH.SetEventNotifier(notifySvc)
	actionItemsH.SetEventNotifier(notifySvc)

	// Metrics registry with DB pool saturation gauges sampled at scrape time.
	reg := metrics.New()
	reg.RegisterGauge("quorum_db_pool_acquired_conns", "Connections currently in use.",
		func() float64 { return float64(pool.Stat().AcquiredConns()) })
	reg.RegisterGauge("quorum_db_pool_idle_conns", "Idle connections in the pool.",
		func() float64 { return float64(pool.Stat().IdleConns()) })
	reg.RegisterGauge("quorum_db_pool_total_conns", "Total connections in the pool.",
		func() float64 { return float64(pool.Stat().TotalConns()) })
	reg.RegisterGauge("quorum_db_pool_max_conns", "Maximum pool size.",
		func() float64 { return float64(pool.Stat().MaxConns()) })

	// Audit-chain health as a metric: 1 intact, 0 BROKEN (alert on == 0),
	// -1 not yet checked. Verification walks the whole chain, so it runs on a
	// slow timer rather than at scrape time.
	var chainIntact atomic.Int64
	chainIntact.Store(-1)
	reg.RegisterGauge("quorum_audit_chain_intact", "1 when the audit log hash chain verifies; 0 when it is broken (tampering).",
		func() float64 { return float64(chainIntact.Load()) })
	go func() {
		check := func() {
			st, err := auditRepo.VerifyChain(ctx)
			switch {
			case err != nil:
				slog.Error("audit chain verification failed to run", "err", err)
			case !st.OK:
				chainIntact.Store(0)
				slog.Error("AUDIT CHAIN BROKEN — possible tampering", "broken_seq", st.BrokenSeq, "entries", st.Entries)
			default:
				chainIntact.Store(1)
			}
		}
		check()
		t := time.NewTicker(6 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				check()
			}
		}
	}()

	r := chi.NewRouter()
	// Note: chi's RealIP middleware is deliberately NOT used — it trusts
	// X-Forwarded-For from any client, which would let attackers rotate the
	// header to bypass the login rate limiter. Rate limiting keys on the raw
	// socket address instead.
	r.Use(handler.RequestID(cfg.TrustProxyHeaders)) // request ids for log correlation
	r.Use(handler.Metrics(reg))                     // request count / latency / in-flight
	r.Use(handler.RequestLogger)                    // structured slog line per request (redacts tokens)
	r.Use(handler.Recoverer(reg))                   // recover panics, count them, log with stack
	r.Use(handler.SecurityHeaders)
	r.Use(handler.MaxRequestBody)

	r.Get("/healthz", handler.Healthz)
	r.Get("/readyz", handler.Readyz(pool))
	// Prometheus exposition, gated by QUORUM_METRICS_TOKEN (disabled when unset).
	r.Get("/metrics", handler.MetricsEndpoint(reg, cfg.MetricsToken))

	r.Route("/api/v1", func(r chi.Router) {
		r.With(mw.LoginRateLimit).Post("/auth/bootstrap", authH.Bootstrap)
		r.With(mw.LoginRateLimit).Post("/auth/login", authH.Login)
		r.With(mw.LoginRateLimit).Post("/auth/login/2fa", authH.LoginMFA)
		r.With(mw.RefreshRateLimit).Post("/auth/refresh", authH.Refresh)

		// Self-service password recovery. Both are unauthenticated and share the
		// tight login limiter: forgot-password always returns 200 (no account
		// enumeration); reset-password consumes a single-use, time-limited token.
		r.With(mw.LoginRateLimit).Post("/auth/forgot-password", authH.ForgotPassword)
		r.With(mw.LoginRateLimit).Post("/auth/reset-password", authH.ResetPassword)

		// Public async-ballot endpoints: a tokenized link lets a member cast a
		// vote without an app session. Rate-limited like the other token flows.
		r.With(mw.RefreshRateLimit).Get("/public/ballot", governanceH.GetBallot)
		r.With(mw.LoginRateLimit).Post("/public/ballot", governanceH.SubmitBallot)

		// Webhooks are unauthenticated but signature-verified.
		r.Post("/webhooks/stripe", webhooksH.Stripe)
		r.Post("/webhooks/paypal", webhooksH.PayPal)

		r.Group(func(r chi.Router) {
			r.Use(mw.Auth)
			r.Use(handler.AuditMiddleware(auditRepo))

			r.Post("/auth/logout", authH.Logout)
			r.Get("/auth/me", authH.Me)
			r.Patch("/auth/me/password", authH.ChangePassword)

			// Two-factor (TOTP) self-management for the authenticated account.
			r.Post("/auth/2fa/setup", authH.Setup2FA)
			r.Post("/auth/2fa/enable", authH.Enable2FA)
			r.Post("/auth/2fa/disable", authH.Disable2FA)
			r.Post("/auth/2fa/recovery-codes", authH.RegenerateRecoveryCodes)

			// Session management: see and revoke your own other devices.
			r.Get("/auth/me/sessions", authH.Sessions)
			r.Post("/auth/me/sessions/revoke-others", authH.RevokeOtherSessions)

			// Personal-data export: any authenticated user may export their own data.
			r.Get("/auth/me/export", exportH.ExportMyData)

			// Notifications are personal: any authenticated user manages their own
			// (no role gate — a restricted member still receives dues notices).
			r.Get("/notifications", notificationsH.List)
			r.Get("/notifications/unread-count", notificationsH.UnreadCount)
			r.Post("/notifications/read-all", notificationsH.MarkAllRead)
			r.Post("/notifications/{id}/read", notificationsH.MarkRead)
			r.Get("/notifications/preferences", notificationsH.GetPreferences)
			r.Put("/notifications/preferences", notificationsH.UpdatePreferences)

			// User management is admin+; minting/altering a superadmin is gated
			// inside the handler. Deleting an account is a superadmin action.
			// Audit log viewer: admin-only, read-only (the log is append-only).
			r.With(mw.RequireRole("admin")).Get("/audit", auditH.List)
			r.With(mw.RequireRole("admin")).Get("/audit/verify", auditH.Verify)
			r.With(mw.RequireRole("admin")).Get("/audit/export.csv", auditH.ExportCSV)

			r.With(mw.RequireRole("admin")).Get("/users", authH.ListUsers)
			r.With(mw.RequireRole("admin")).Post("/users", authH.CreateUser)
			r.With(mw.RequireRole("admin")).Patch("/users/{id}", authH.UpdateUser)
			r.With(mw.RequireRole("admin")).Post("/users/{id}/reset-password", authH.AdminResetPassword)
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
			// Right to erasure: strips personal data in place, keeping the
			// financial and governance records that reference the member intact
			// (see MembersRepo.Erase). Irreversible, so superadmin only.
			r.With(mw.RequireRole("superadmin")).Post("/members/{id}/erase", membersH.Erase)
			r.Get("/members/{id}/dues", membersH.GetDues)                // ownership-scoped
			r.Get("/members/{id}/action-items", membersH.GetActionItems) // ownership-scoped

			r.With(mw.RequireRole("officer")).Get("/dues", duesH.List)
			r.With(mw.RequireRole("officer")).Post("/dues", duesH.Create)
			r.With(mw.RequireRole("officer")).Get("/dues/{id}", duesH.Get)
			r.With(mw.RequireRole("officer")).Patch("/dues/{id}", duesH.Update)
			r.With(mw.RequireRole("officer")).Post("/dues/{id}/transactions", duesH.CreateTransaction)
			r.With(mw.RequireRole("officer")).Get("/dues/transactions", duesH.ListTransactions)

			// Recurring dues schedules (auto-generate invoices per tier).
			r.With(mw.RequireRole("officer")).Get("/dues/schedules", duesH.ListSchedules)
			r.With(mw.RequireRole("officer")).Post("/dues/schedules", duesH.CreateSchedule)
			r.With(mw.RequireRole("officer")).Patch("/dues/schedules/{id}", duesH.UpdateSchedule)
			r.With(mw.RequireRole("officer")).Delete("/dues/schedules/{id}", duesH.DeleteSchedule)
			r.With(mw.RequireRole("officer")).Post("/dues/schedules/{id}/generate", duesH.GenerateSchedule)

			// Budget scenario planning (officer+). Static paths (compare) are
			// registered before the {id} param route so chi matches them first.
			r.With(mw.RequireRole("officer")).Get("/budgets", budgetH.List)
			r.With(mw.RequireRole("officer")).Post("/budgets", budgetH.Create)
			r.With(mw.RequireRole("officer")).Get("/budgets/compare", budgetH.Compare)
			r.With(mw.RequireRole("officer")).Get("/budgets/{id}", budgetH.Get)
			r.With(mw.RequireRole("officer")).Patch("/budgets/{id}", budgetH.Update)
			r.With(mw.RequireRole("officer")).Delete("/budgets/{id}", budgetH.Delete)
			r.With(mw.RequireRole("officer")).Post("/budgets/{id}/clone", budgetH.Clone)
			r.With(mw.RequireRole("officer")).Post("/budgets/{id}/seed-dues", budgetH.SeedDues)
			r.With(mw.RequireRole("officer")).Post("/budgets/{id}/lines", budgetH.AddLine)
			r.With(mw.RequireRole("officer")).Patch("/budget-lines/{id}", budgetH.UpdateLine)
			r.With(mw.RequireRole("officer")).Delete("/budget-lines/{id}", budgetH.DeleteLine)

			// Analytics dashboard aggregates (officer+ — a leadership overview).
			r.With(mw.RequireRole("officer")).Get("/analytics/overview", analyticsH.Overview)
			r.With(mw.RequireRole("officer")).Get("/analytics/membership", analyticsH.Membership)
			r.With(mw.RequireRole("officer")).Get("/analytics/attendance", analyticsH.Attendance)
			r.With(mw.RequireRole("officer")).Get("/analytics/governance", analyticsH.Governance)
			r.With(mw.RequireRole("officer")).Get("/analytics/payments", analyticsH.Payments)

			// Multi-currency: reporting currency + exchange rates. Reads are
			// officer+ (they drive the same financial dashboards); changes are
			// admin+ because they reprice every aggregate.
			r.With(mw.RequireRole("officer")).Get("/fx/settings", fxH.GetSettings)
			r.With(mw.RequireRole("admin")).Put("/fx/settings", fxH.UpdateSettings)
			r.With(mw.RequireRole("officer")).Get("/fx/rates", fxH.ListRates)
			r.With(mw.RequireRole("admin")).Post("/fx/rates", fxH.CreateRate)
			r.With(mw.RequireRole("admin")).Delete("/fx/rates/{id}", fxH.DeleteRate)

			// CSV data exports. Member roster is visible to members and up; the
			// financial exports (dues, transactions) require officer and up.
			r.With(mw.RequireRole("member")).Get("/export/members.csv", exportH.ExportMembersCSV)
			r.With(mw.RequireRole("officer")).Get("/export/dues.csv", exportH.ExportDuesCSV)
			r.With(mw.RequireRole("officer")).Get("/export/transactions.csv", exportH.ExportTransactionsCSV)

			r.With(mw.RequireRole("member")).Get("/meetings", meetingsH.List)
			r.With(mw.RequireRole("officer")).Post("/meetings", meetingsH.Create)
			r.With(mw.RequireRole("member")).Get("/meetings/{id}", meetingsH.Get)
			r.With(mw.RequireRole("officer")).Patch("/meetings/{id}", meetingsH.Update)
			r.With(mw.RequireRole("superadmin")).Delete("/meetings/{id}", meetingsH.Delete)
			r.With(mw.RequireRole("officer")).Put("/meetings/{id}/attendees", meetingsH.SetAttendees)
			r.With(mw.RequireRole("officer")).Post("/meetings/{id}/decisions", meetingsH.CreateDecision)
			r.With(mw.RequireRole("officer")).Patch("/meetings/{id}/decisions/{did}", meetingsH.UpdateDecision)
			// Recorded decisions are the minutes; removing one is a superadmin act.
			r.With(mw.RequireRole("superadmin")).Delete("/meetings/{id}/decisions/{did}", meetingsH.DeleteDecision)

			// Governance & voting. Reads are member+ (so members can watch the
			// live quorum meter and cast their own ballots); managing motions,
			// proxies, and settings is officer/admin.
			r.With(mw.RequireRole("member")).Get("/governance/settings", governanceH.GetSettings)
			r.With(mw.RequireRole("admin")).Put("/governance/settings", governanceH.UpdateSettings)

			r.With(mw.RequireRole("member")).Get("/meetings/{id}/quorum", governanceH.Quorum)

			r.With(mw.RequireRole("member")).Get("/meetings/{id}/motions", governanceH.ListMotions)
			r.With(mw.RequireRole("officer")).Post("/meetings/{id}/motions", governanceH.CreateMotion)
			r.With(mw.RequireRole("member")).Get("/motions/{id}", governanceH.GetMotion)
			r.With(mw.RequireRole("officer")).Patch("/motions/{id}", governanceH.UpdateMotion)
			r.With(mw.RequireRole("officer")).Delete("/motions/{id}", governanceH.DeleteMotion)
			r.With(mw.RequireRole("officer")).Post("/motions/{id}/second", governanceH.SecondMotion)
			r.With(mw.RequireRole("officer")).Post("/motions/{id}/open", governanceH.OpenMotion)
			r.With(mw.RequireRole("officer")).Post("/motions/{id}/close", governanceH.CloseMotion)
			// A member casts their OWN ballot; an officer records on behalf / tallies.
			r.With(mw.RequireRole("member")).Post("/motions/{id}/vote", governanceH.CastVote)
			r.With(mw.RequireRole("officer")).Post("/motions/{id}/votes", governanceH.RecordVote)
			// Email single-use ballot links so members (incl. restricted) vote async.
			r.With(mw.RequireRole("officer")).Post("/motions/{id}/ballots", governanceH.SendBallots)

			r.With(mw.RequireRole("member")).Get("/meetings/{id}/proxies", governanceH.ListProxies)
			r.With(mw.RequireRole("officer")).Post("/meetings/{id}/proxies", governanceH.CreateProxy)
			r.With(mw.RequireRole("officer")).Delete("/proxies/{id}", governanceH.DeleteProxy)

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
			r.With(mw.RequireRole("superadmin")).Delete("/plans/{id}/decisions/{did}", plansH.DeleteDecision)

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
	// Slightly above the server WriteTimeout (30s), so a slow-but-legitimate
	// in-flight request can finish rather than being cut off mid-response.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		// Keep going: exiting here would skip draining the notifier queues and
		// the scheduler, losing queued notices for the sake of a slow request.
		log.Printf("shutdown: %v (continuing cleanup)", err)
	}

	// Drain any in-flight notices (deletion + event), then stop the scheduler,
	// all before the deferred pool.Close() so nothing runs against a closed pool.
	notifier.Close()
	notifySvc.Close()
	cancel()
	select {
	case <-schedDone:
	case <-time.After(5 * time.Second):
		log.Println("scheduler did not stop in time")
	}
	log.Println("done")
}

// staticHandler serves the embedded web assets. The application root ("/")
// serves index.html; any other directory path (a trailing slash or a path that
// resolves to a directory) returns 404 so no auto-generated listing is exposed.
// A short cache lifetime lets same-session revisits skip re-downloading
// unchanged JS/CSS.
// runUnlock2FA is the break-glass path for an account locked out of two-factor:
// it clears the TOTP secret, the enabled flag, and all recovery codes, so the
// user can sign in with their password alone and re-enroll. It also revokes that
// user's refresh tokens, since anyone holding one would otherwise keep a session
// that predates the change.
//
// It requires shell access to the deployment (and therefore the database), which
// is the intended bar: this bypasses a second factor.
func runUnlock2FA(ctx context.Context, pool *pgxpool.Pool, email string) error {
	authRepo := repo.NewAuthRepo(pool)
	user, _, err := authRepo.GetUserByEmail(ctx, strings.ToLower(strings.TrimSpace(email)))
	if err != nil {
		return fmt.Errorf("no user with email %q: %w", email, err)
	}
	if err := authRepo.DisableTOTP(ctx, user.ID); err != nil {
		return fmt.Errorf("disable totp: %w", err)
	}
	if err := authRepo.RevokeAllRefreshTokensForUser(ctx, user.ID); err != nil {
		return fmt.Errorf("revoke sessions: %w", err)
	}
	// The single most sensitive bypass in the system must leave a durable trail:
	// record it in the audit log (user_id = the affected account; there is no
	// HTTP actor for a CLI one-shot).
	if err := repo.NewAuditRepo(pool).Log(ctx, user.ID, "auth.2fa_unlocked_breakglass", "auth", user.ID, nil); err != nil {
		return fmt.Errorf("audit break-glass: %w", err)
	}
	log.Printf("two-factor disabled and sessions revoked for %s (%s) — they can now sign in with their password and re-enroll",
		user.Email, user.Role)
	return nil
}

// logLevel maps a config log-level string to an slog.Level (default info).
func logLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func staticHandler(webFS fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(webFS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The root serves the app shell (hash routing means the browser always
		// requests "/" for navigation). Do not treat it as a directory listing.
		if r.URL.Path == "/" {
			w.Header().Set("Cache-Control", "public, max-age=300")
			fileServer.ServeHTTP(w, r)
			return
		}
		// Any other trailing-slash path is a directory request → 404.
		if strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		clean := strings.TrimPrefix(r.URL.Path, "/")
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
