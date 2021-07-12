package main

import (
	"github.com/jay-dee7/parachute/registry/v2"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/fatih/color"
	"github.com/jay-dee7/parachute/auth"
	"github.com/jay-dee7/parachute/cache"
	"github.com/jay-dee7/parachute/config"
	"github.com/jay-dee7/parachute/skynet"
	"github.com/labstack/echo-contrib/prometheus"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/rs/zerolog"
)

func main() {
	var configPath string
	if len(os.Args) != 2 {
		configPath = "./"
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		color.Red("error reading cfg file: %s", err.Error())
		os.Exit(1)
	}

	e := echo.New()
	p := prometheus.NewPrometheus("echo", nil)
	p.Use(e)
	e.HideBanner = true

	// e.Use(middleware.HTTPSNonWWWRedirect())
	// e.Use(middleware.HTTPSRedirect())

	l := setupLogger()
	localCache, err := cache.New("/tmp/badger")
	if err != nil {
		l.Err(err).Send()
		return
	}
	defer localCache.Close()

	authSvc := auth.New(localCache, cfg)

	skynetClient := skynet.NewClient(cfg)

	reg, err := registry.NewRegistry(skynetClient, l, localCache, e.Logger)
	if err != nil {
		l.Err(err).Send()
		return
	}
	e.Use(middleware.LoggerWithConfig(middleware.LoggerConfig{
		Skipper: func(echo.Context) bool {
			return false
		},
		Format: "method=${method}, uri=${uri}, status=${status} latency=${latency}\n",
		Output: os.Stdout,
	}))

	e.Use(middleware.RateLimiterWithConfig(middleware.RateLimiterConfig{
		Skipper:    middleware.DefaultSkipper,
		BeforeFunc: nil,
		IdentifierExtractor: func(ctx echo.Context) (string, error) {
			return ctx.RealIP(), nil
		},
		Store: middleware.NewRateLimiterMemoryStoreWithConfig(middleware.RateLimiterMemoryStoreConfig{
			Rate:      3,
			Burst:     0,
			ExpiresIn: time.Hour * 10,
		}),
		ErrorHandler: func(ctx echo.Context, err error) error {
			return ctx.JSON(http.StatusForbidden, echo.Map{
				"error": "Too many requests, try after some time!",
			})
		},
	}))

	e.Use(middleware.Recover())

	e.Use(middleware.CORS())

	internal := e.Group("/internal")
	authRouter := e.Group("/auth")
	betaRouter := e.Group("/beta")
	authRouter.Add(http.MethodPost, "/signup", authSvc.SignUp)
	authRouter.Add(http.MethodPost, "/signin", authSvc.SignIn)
	authRouter.Add(http.MethodPost, "/token", authSvc.SignIn)

	betaRouter.Add(http.MethodPost, "/register", localCache.RegisterForBeta)
	betaRouter.Add(http.MethodGet, "/register", localCache.GetAllEmail)

	internal.Add(http.MethodGet, "/metadata", localCache.Metadata)
	internal.Add(http.MethodGet, "/digests", localCache.LayerDigests)

	router := e.Group("/v2/:username/:imagename")
	router.Use(BasicAuth(authSvc.BasicAuth))

	// ALL THE HEAD METHODS //
	// HEAD /v2/<name>/blobs/<digest>
	router.Add(http.MethodHead, "/blobs/:digest", reg.LayerExists) // (LayerExists) should be called reference/digest

	// HEAD /v2/<name>/manifests/<reference>
	router.Add(http.MethodHead, "/manifests/:reference", reg.ManifestExists) //should be called reference/digest

	// ALL THE PUT METHODS
	// PUT /v2/<name>/blobs/uploads/<uuid>?digest=<digest>
	// router.Add(http.MethodPut, "/blobs/uploads/:uuid", reg.MonolithicUpload)

	router.Add(http.MethodPut, "/blobs/uploads/", reg.CompleteUpload)

	// PUT /v2/<name>/blobs/uploads/<uuid>?digest=<digest>
	router.Add(http.MethodPut, "/blobs/uploads/:uuid", reg.CompleteUpload)

	// PUT /v2/<name>/manifests/<reference>
	router.Add(http.MethodPut, "/manifests/:reference", reg.PushManifest)

	// POST METHODS
	// POST /v2/<name>/blobs/uploads/
	router.Add(http.MethodPost, "/blobs/uploads/", reg.StartUpload)

	// POST /v2/<name>/blobs/uploads/
	router.Add(http.MethodPost, "/blobs/uploads/:uuid", reg.PushLayer)

	// PATCH

	// PATCH /v2/<name>/blobs/uploads/<uuid>
	router.Add(http.MethodPatch, "/blobs/uploads/:uuid", reg.ChunkedUpload)
	// router.Add(http.MethodPatch, "/blobs/uploads/", reg.ChunkedUpload)

	// GET
	// GET /v2/<name>/manifests/<reference>
	router.Add(http.MethodGet, "/manifests/:reference", reg.PullManifest)

	// GET /v2/<name>/blobs/<digest>
	router.Add(http.MethodGet, "/blobs/:digest", reg.PullLayer)

	// GET GET /v2/<name>/blobs/uploads/<uuid>
	router.Add(http.MethodGet, "/blobs/uploads/:uuid", reg.UploadProgress)

	// router.Add(http.MethodGet, "/blobs/:digest", reg.DownloadBlob)

	e.Add(http.MethodGet, "/v2/", reg.ApiVersion, BasicAuth(authSvc.BasicAuth))

	log.Println(e.Start(cfg.Address()))
}

func setupLogger() zerolog.Logger {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	zerolog.SetGlobalLevel(zerolog.DebugLevel)
	l := zerolog.New(os.Stdout)
	l.With().Caller().Logger()

	return l
}

//when we use JWT
/*AuthMiddleware
HTTP/1.1 401 Unauthorized
Content-Type: application/json; charset=utf-8
Docker-Distribution-Api-Version: registry/2.0
Www-Authenticate: Bearer realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:samalba/my-app:pull,push"
Date: Thu, 10 Sep 2015 19:32:31 GMT
Content-Length: 235
Strict-Transport-Security: max-age=31536000

{"errors":[{"code":"UNAUTHORIZED","message":"","detail":}]}
*/
//var wwwAuthenticate = `Bearer realm="http://0.0.0.0:5000/auth/token",service="http://0.0.0.0:5000",scope="repository:%s`

func BasicAuth(authfn func(string, string) (map[string]interface{}, error)) echo.MiddlewareFunc {
	return middleware.BasicAuth(func(username string, password string, ctx echo.Context) (bool, error) {

		if ctx.Request().RequestURI != "/v2/" {
			if ctx.Request().Method == http.MethodHead || ctx.Request().Method == http.MethodGet {
				return true, nil
			}
		}

		if ctx.Request().RequestURI == "/v2/" {
			_, err := authfn(username, password)
			if err != nil {
				return false, ctx.NoContent(http.StatusUnauthorized)
			}
			return true, nil
		}

		usernameFromNameSpace := ctx.Param("username")
		if usernameFromNameSpace != username {
			var errMsg registry.RegistryErrors
			errMsg.Errors = append(errMsg.Errors, registry.RegistryError{
				Code:    registry.RegistryErrorCodeDenied,
				Message: "not authorised",
				Detail:  nil,
			})
			return false, ctx.JSON(http.StatusForbidden, errMsg)
		}
		resp, err := authfn(username, password)
		if err != nil {
			return false, err
		}

		ctx.Set("basic_auth", resp)
		return true, nil
	})
}
