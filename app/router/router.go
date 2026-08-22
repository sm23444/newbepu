package router

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/memstore"
	"github.com/gin-gonic/gin"
	"github.com/spf13/cast"
	"github.com/v03413/bepusdt/app/conf"
	"github.com/v03413/bepusdt/app/log"
	"github.com/v03413/bepusdt/app/model"
	"github.com/v03413/go-cache"
)

var engine *gin.Engine
var authRoute = make(map[string]bool)
var secureRoute = make(map[string]struct{})

const maxRequestBodyBytes int64 = 1 << 20
const maxReviewRequestBodyBytes int64 = 6 << 20

func Handler() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)

	engine = gin.New()
	if err := configureTrustedProxies(engine, os.Getenv("BEPUSDT_TRUSTED_PROXIES")); err != nil {
		log.Warn("invalid BEPUSDT_TRUSTED_PROXIES; forwarded client IP headers are disabled:", err)
		_ = engine.SetTrustedProxies(nil)
	}
	engine.Use(limitRequestBody())
	session := memstore.NewStore([]byte(model.GetK(model.AdminSecret)))
	session.Options(sessions.Options{
		MaxAge:   86400,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Path:     "/",
	})

	engine.Use(sessions.Sessions("session", session))
	engine.Use(gin.LoggerWithWriter(log.GetWriter()), gin.Recovery())
	engine.Use(sessionAuth(), copyright())
	engine.NoRoute(noRoute())
	engine.GET("/", func(ctx *gin.Context) {
		if !model.IsInstalled() {
			ctx.Header("Cache-Control", "no-store")
			ctx.HTML(200, "installed.html", gin.H{})

			return
		}

		sess := sessions.Default(ctx)
		if secure, ok := sess.Get(conf.AdminSecureK).(bool); ok && secure {
			ctx.HTML(200, "secure.html", gin.H{})
			return
		}

		if url := model.GetC(model.HomeRedirectUrl); url != "" {
			ctx.Redirect(302, url)
			return
		}

		ctx.HTML(200, "index.html", gin.H{"title": conf.Desc, "url": conf.Github})
	})

	{
		staticInit(engine)
		installInit(engine)
		epusdtInit(engine)
		epayInit(engine)
		adminInit(engine)
		authInit(engine)
	}

	return engine
}

func configureTrustedProxies(engine *gin.Engine, raw string) error {
	proxies := make([]string, 0)
	for _, proxy := range strings.Split(raw, ",") {
		if proxy = strings.TrimSpace(proxy); proxy != "" {
			proxies = append(proxies, proxy)
		}
	}
	if len(proxies) == 0 {
		proxies = nil
	}
	return engine.SetTrustedProxies(proxies)
}

func limitRequestBody() gin.HandlerFunc {
	return func(ctx *gin.Context) {
		if ctx.Request.Body != nil {
			limit := maxRequestBodyBytes
			if strings.HasPrefix(ctx.Request.URL.Path, "/api/v1/pay/payment-review") {
				limit = maxReviewRequestBodyBytes
			}
			ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, limit)
		}
		ctx.Next()
	}
}

func sessionAuth() gin.HandlerFunc {
	return func(ctx *gin.Context) {
		if conf.Debug {
			ctx.Next()
			return
		}

		var route = fmt.Sprintf("%s.%s", ctx.Request.Method, ctx.Request.URL.Path)
		if _, ok := secureRoute[route]; ok {
			sess := sessions.Default(ctx)
			if secure, ok := sess.Get(conf.AdminSecureK).(bool); !ok || !secure {
				ctx.JSON(403, gin.H{"code": 403, "msg": "unauthorized access"})
				ctx.Abort()
				return
			}
		}

		var need, ok = authRoute[route]
		if !ok || !need {
			ctx.Next()
			return
		}

		token, ok := cache.Get(conf.AdminTokenK)
		if !ok {
			ctx.JSON(403, gin.H{"code": 403, "msg": "token expired, please login again"})
			ctx.Abort()
			return
		}

		authHeader := ctx.GetHeader("Authorization")
		if authHeader == "" {
			ctx.JSON(403, gin.H{"code": 403, "msg": "missing authorization token"})
			ctx.Abort()
			return
		}

		if subtle.ConstantTimeCompare([]byte(cast.ToString(token)), []byte(authHeader)) != 1 {
			ctx.JSON(403, gin.H{"code": 403, "msg": "invalid authorization token"})
			ctx.Abort()
			return
		}

		ctx.Next()
	}
}

func noRoute() gin.HandlerFunc {
	return func(ctx *gin.Context) {
		if model.IsInstalled() && ctx.Request.URL.Path == model.GetC(model.AdminSecure) {
			session := sessions.Default(ctx)
			session.Set(conf.AdminSecureK, true)
			_ = session.Save()

			ctx.Redirect(302, "/#/login")

			return
		}
	}
}

func copyright() gin.HandlerFunc {
	return func(ctx *gin.Context) {
		ctx.Writer.Header().Set("Payment-Gateway", "https://github.com/sm23444/newbepu")
	}
}

func PostRegister(router *gin.RouterGroup, relativePath string, checkAuth bool, handlers ...gin.HandlerFunc) {
	var route = fmt.Sprintf("POST.%s%s", router.BasePath(), relativePath)

	authRoute[route] = checkAuth
	secureRoute[route] = struct{}{}

	router.POST(relativePath, handlers...)
}

func GetRegister(router *gin.RouterGroup, relativePath string, checkAuth bool, handlers ...gin.HandlerFunc) {
	var route = fmt.Sprintf("GET.%s%s", router.BasePath(), relativePath)

	authRoute[route] = checkAuth
	secureRoute[route] = struct{}{}

	router.GET(relativePath, handlers...)
}
