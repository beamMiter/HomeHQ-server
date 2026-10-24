package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/beamMiter/HomeHQ-server/internal/appliances"
	"github.com/beamMiter/HomeHQ-server/internal/auth"
	"github.com/beamMiter/HomeHQ-server/internal/bills"
	"github.com/beamMiter/HomeHQ-server/internal/cameras"
	"github.com/beamMiter/HomeHQ-server/internal/chores"
	"github.com/beamMiter/HomeHQ-server/internal/config"
	"github.com/beamMiter/HomeHQ-server/internal/contractors"
	"github.com/beamMiter/HomeHQ-server/internal/dashboard"
	"github.com/beamMiter/HomeHQ-server/internal/db"
	"github.com/beamMiter/HomeHQ-server/internal/files"
	"github.com/beamMiter/HomeHQ-server/internal/health"
	"github.com/beamMiter/HomeHQ-server/internal/httpx"
	"github.com/beamMiter/HomeHQ-server/internal/medicine"
	"github.com/beamMiter/HomeHQ-server/internal/notes"
	"github.com/beamMiter/HomeHQ-server/internal/places"
	"github.com/beamMiter/HomeHQ-server/internal/purchases"
	"github.com/beamMiter/HomeHQ-server/internal/storage"
	"github.com/beamMiter/HomeHQ-server/internal/user"
)

func main() {
	cfg := config.Load()
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	// In production an unsafe setting (default secret, local database, open
	// CORS) is a reason not to start, not a line in the log.
	if err := cfg.Validate(); err != nil {
		logger.Fatal("unsafe production configuration", zap.Error(err))
	}
	if cfg.IsProduction() && cfg.StorageBackend == "local" {
		logger.Warn("STORAGE_BACKEND=local: uploads vanish with the container on most hosts; use r2")
	}

	ctx := context.Background()
	pool, err := db.Connect(ctx, cfg.DSN)
	if err != nil {
		logger.Fatal("db connect", zap.Error(err))
	}
	defer pool.Close()

	// Users & Auth
	uRepo := user.Repo{DB: pool}
	userGuard := auth.NewUserGuard(uRepo.TokenVersion)
	uHandler := user.Handler{
		Repo:      uRepo,
		JWTSecret: cfg.JWTSecret,
		Throttle:  user.NewThrottle(),
		// No email is sent yet, so a reset token can only be read from the log:
		// fine on a laptop, a hole anywhere else.
		DevResetTokens: !cfg.IsProduction(),
		// A password change must end old sessions now, not when the guard's
		// minute of remembering is up.
		OnRevoke: userGuard.Forget,
	}

	// Notes
	nRepo := notes.Repo{DB: pool}
	nHandler := notes.Handler{Repo: nRepo}

	// Files
	st := storage.New(cfg)
	fRepo := files.Repo{DB: pool}
	fHandler := files.Handler{Repo: fRepo, Storage: st, JWTSecret: cfg.JWTSecret}

	// Purchases
	pRepo := purchases.NewRepo(pool)
	pSvc := purchases.NewService(pRepo)
	pHandler := purchases.Handler{Svc: pSvc}

	// Bills
	bRepo := bills.Repo{DB: pool}
	bSvc := bills.NewService(bRepo)
	bHandler := bills.Handler{Svc: bSvc}

	// Contractors
	ctrRepo := contractors.NewRepo(pool)
	ctrSvc := contractors.NewService(ctrRepo)
	ctrH := contractors.NewHandler(ctrSvc)

	// Google Places (off unless GOOGLE_PLACES_API_KEY is set)
	placesLimits := places.DefaultLimits()
	if cfg.GooglePlacesDailyLimit > 0 {
		placesLimits.DailyCalls = cfg.GooglePlacesDailyLimit
	}
	if cfg.GooglePlacesRatePerMin > 0 {
		placesLimits.RatePerMin = cfg.GooglePlacesRatePerMin
	}
	if cfg.GooglePlacesMaxRadiusM > 0 {
		placesLimits.MaxRadiusM = float64(cfg.GooglePlacesMaxRadiusM)
	}
	placesLimits.AllowAllTrades = cfg.GooglePlacesAllowAllTrades
	placesSvc := places.NewService(
		places.NewClient(cfg.GooglePlacesAPIKey, cfg.GooglePlacesBaseURL),
		places.NewPGStore(pool),
		placesLimits,
	)
	placesH := places.NewHandler(placesSvc)
	if !placesSvc.Enabled() {
		logger.Info("google places disabled: GOOGLE_PLACES_API_KEY not set")
	}

	// Cameras (off unless an Imou account is configured, or CAMERAS_DRIVER=fake)
	var camDriver cameras.Driver
	switch kind := cfg.CamerasDriver; {
	case kind == "fake":
		fake := cameras.NewFake(cfg.CamerasFakeStreamURL)
		fake.Pictures = cfg.CamerasFakePictures
		camDriver = fake
	case kind == "imou" || (kind == "" && cfg.ImouAppID != "" && cfg.ImouAppSecret != ""):
		camDriver = cameras.NewImou(cameras.ImouConfig{
			AppID:     cfg.ImouAppID,
			AppSecret: cfg.ImouAppSecret,
			BaseURL:   cfg.ImouBaseURL,
			Region:    cfg.ImouRegion,
		})
	}
	camSvc := cameras.NewService(camDriver)
	camH := cameras.NewHandler(camSvc)
	if camSvc.Enabled() {
		logger.Info("cameras enabled", zap.String("driver", camDriver.Name()))
	} else {
		logger.Info("cameras disabled: set IMOU_APP_ID/IMOU_APP_SECRET (or CAMERAS_DRIVER=fake)")
	}

	// Appliances (off unless APPLIANCES_DRIVER names a driver)
	var appDriver appliances.Driver
	switch kind := cfg.AppliancesDriver; kind {
	case "":
	case "fake":
		appDriver = appliances.NewFake()
	case "local":
		appDriver = appliances.NewLocal(appliances.NewPGRegistry(pool))
	default:
		logger.Warn("unknown APPLIANCES_DRIVER, appliances stay off", zap.String("driver", kind))
	}
	appSvc := appliances.NewService(appDriver, appliances.NewPGStore(pool))
	appH := appliances.NewHandler(appSvc)
	if appSvc.Enabled() {
		logger.Info("appliances enabled", zap.String("driver", appDriver.Name()))
	} else {
		logger.Info("appliances disabled: set APPLIANCES_DRIVER=local to add your own (or fake for a demo set)")
	}

	// Spending dashboard (bills + purchases)
	dashH := dashboard.NewHandler(dashboard.NewPGStore(pool))

	// Medicine
	mRepo := medicine.NewRepo(pool)
	mSvc := medicine.NewService(mRepo)
	mHandler := medicine.NewHandler(mSvc)

	// Chores
	choresHandler := chores.Handler{Repo: chores.Repo{DB: pool}}

	// Router
	r := chi.NewRouter()
	for _, m := range httpx.CommonMiddlewares(httpx.Options{
		CorsOrigin:       cfg.CorsOrigin,
		Development:      !cfg.IsProduction(),
		TrustedProxyHops: cfg.TrustedProxyHops,
		// Everything is small JSON except the one upload route.
		MaxBody:   httpx.DefaultMaxBody,
		BodyRules: []httpx.BodyRule{{Path: "/api/v1/uploads", Limit: files.MaxUploadBody}},
	}) {
		r.Use(m)
	}

	r.Get("/healthz", health.Live)
	r.Get("/readyz", health.Ready)

	// The sign-in routes take a burst of 20 calls per address, then
	// AUTH_RATE_LIMIT_PER_MIN a minute. (Wrong passwords are counted separately
	// and much tighter: user.Throttle.)
	authLimit := func(next http.Handler) http.Handler { return next }
	if cfg.AuthRatePerMin > 0 {
		authLimit = httpx.RateLimit(httpx.NewLimiter(float64(cfg.AuthRatePerMin), 20), httpx.RemoteIP)
	}

	r.Route("/api/v1", func(api chi.Router) {
		// Public
		api.Group(func(pub chi.Router) {
			pub.Use(authLimit)

			// Sign-up is closed. Every account can read all of the household's
			// data (bills, purchases, cameras, medicine), so letting anyone who
			// finds the address create one would hand it over. Accounts are made
			// by the owner with `go run ./cmd/adduser`. The route is kept here,
			// not deleted, for a future closed sign-up (an invite code, say);
			// the handler is user.Handler.Register.
			// pub.Post("/auth/register", uHandler.Register)
			pub.Post("/auth/login", uHandler.Login)
			pub.Post("/auth/refresh", uHandler.Refresh)
			pub.Post("/auth/forgot-password", uHandler.ForgotPassword)
			pub.Post("/auth/reset-password", uHandler.ResetPassword)
		})

		// Auth required
		api.Group(func(pr chi.Router) {
			pr.Use(auth.RequireAuth(cfg.JWTSecret, auth.NewClaims))
			// A valid token whose account is gone, or whose password has been
			// changed since, gets a 401, not a failure further in (see
			// auth.RequireUser).
			pr.Use(userGuard.Middleware)

			pr.Get("/me", uHandler.Me)
			pr.Patch("/me", uHandler.UpdateProfile)
			pr.Post("/me/password", uHandler.ChangePassword)
			pr.Post("/auth/logout", uHandler.Logout)

			nHandler.RegisterRoutes(pr)
			fHandler.RegisterRoutes(pr)
			choresHandler.RegisterRoutes(pr)
			pHandler.RegisterRoutes(pr)
			bHandler.RegisterRoutes(pr)
			dashH.RegisterRoutes(pr)

			pr.Route("/medicine", func(r chi.Router) {
				mHandler.RegisterRoutes(r)
			})

			pr.Route("/contractors", func(r chi.Router) {
				ctrH.RegisterRoutes(r)
			})

			pr.Route("/places", func(r chi.Router) {
				placesH.RegisterRoutes(r)
			})

			pr.Route("/cameras", func(r chi.Router) {
				camH.RegisterRoutes(r)
			})

			pr.Route("/appliances", func(r chi.Router) {
				appH.RegisterRoutes(r)
			})
		})
	})

	// Every limit here bounds how long one slow or hostile connection can hold
	// the server. Reads get long enough for a photo over a poor mobile link.
	srv := &http.Server{
		Addr:              ":" + cfg.AppPort,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      90 * time.Second, // above the 60 s per-request timeout
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    32 << 10,
	}

	// Hosts stop a container with SIGTERM and give it a few seconds: finish the
	// requests in flight rather than cutting them off.
	done := make(chan struct{})
	go func() {
		defer close(done)
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
		<-stop
		logger.Info("shutting down")
		sctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if err := srv.Shutdown(sctx); err != nil {
			logger.Warn("shutdown", zap.Error(err))
		}
	}()

	logger.Info("listening", zap.String("addr", srv.Addr), zap.Bool("production", cfg.IsProduction()))
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Fatal("server", zap.Error(err))
	}
	<-done
}
