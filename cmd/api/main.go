package main

import (
	"context"
	"errors"
	"net/http"
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

	ctx := context.Background()
	pool, err := db.Connect(ctx, cfg.DSN)
	if err != nil {
		logger.Fatal("db connect", zap.Error(err))
	}
	defer pool.Close()

	// Users & Auth
	uRepo := user.Repo{DB: pool}
	uHandler := user.Handler{Repo: uRepo, JWTSecret: cfg.JWTSecret}

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
	for _, m := range httpx.CommonMiddlewares(cfg.CorsOrigin) {
		r.Use(m)
	}

	r.Get("/healthz", health.Live)
	r.Get("/readyz", health.Ready)

	r.Route("/api/v1", func(api chi.Router) {
		// Public
		api.Post("/auth/register", uHandler.Register)
		api.Post("/auth/login", uHandler.Login)
		api.Post("/auth/refresh", uHandler.Refresh)
		api.Post("/auth/forgot-password", uHandler.ForgotPassword)
		api.Post("/auth/reset-password", uHandler.ResetPassword)

		// Auth required
		api.Group(func(pr chi.Router) {
			pr.Use(auth.RequireAuth(cfg.JWTSecret, auth.NewClaims))
			// A valid token whose account is gone gets a 401, not a failure
			// further in (see auth.RequireUser).
			pr.Use(auth.RequireUser(func(ctx context.Context, id string) (bool, error) {
				_, err := uRepo.ByID(ctx, id)
				if errors.Is(err, user.ErrNotFound) {
					return false, nil
				}
				return err == nil, err
			}))

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

	srv := &http.Server{
		Addr:              ":" + cfg.AppPort,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
	}
	logger.Info("listening", zap.String("addr", srv.Addr))
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Fatal("server", zap.Error(err))
	}
}
