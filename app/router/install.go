package router

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/v03413/bepusdt/app/log"
	"github.com/v03413/bepusdt/app/model"
)

const maxInstallFormBytes int64 = 8 << 10

type installForm struct {
	InstallToken    string `form:"install_token" binding:"required"`
	Username        string `form:"username" binding:"required"`
	Password        string `form:"password" binding:"required"`
	ConfirmPassword string `form:"confirm_password" binding:"required"`
}

func installInit(e *gin.Engine) {
	e.POST("/install", completeInstall)
}

func completeInstall(ctx *gin.Context) {
	ctx.Header("Cache-Control", "no-store")
	if model.IsInstalled() {
		ctx.Redirect(http.StatusSeeOther, "/")
		return
	}

	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, maxInstallFormBytes)
	var form installForm
	if err := ctx.ShouldBind(&form); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			renderInstallError(ctx, http.StatusRequestEntityTooLarge, "", "安装请求内容过大")
			return
		}
		renderInstallError(ctx, http.StatusBadRequest, "", "安装表单不完整")
		return
	}

	username := strings.TrimSpace(form.Username)
	if form.Password != form.ConfirmPassword {
		renderInstallError(ctx, http.StatusBadRequest, username, "两次输入的密码不一致")
		return
	}

	secure, err := model.CompleteInstall(form.InstallToken, username, form.Password)
	if err != nil {
		switch {
		case errors.Is(err, model.ErrInvalidInstallToken):
			renderInstallError(ctx, http.StatusForbidden, username, "一次性安装令牌无效")
		case errors.Is(err, model.ErrInvalidInstallUser):
			renderInstallError(ctx, http.StatusBadRequest, username, "管理员账号不能为空且不能超过 32 个字符")
		case errors.Is(err, model.ErrInvalidInstallPasswd):
			renderInstallError(ctx, http.StatusBadRequest, username, "管理员密码至少需要 6 个字符且不能过长")
		case errors.Is(err, model.ErrAlreadyInstalled):
			ctx.Redirect(http.StatusSeeOther, "/")
		default:
			log.Error("complete secure installation:", err)
			renderInstallError(ctx, http.StatusInternalServerError, username, "初始化失败，请检查服务日志后重试")
		}
		return
	}

	ctx.Redirect(http.StatusSeeOther, secure)
}

func renderInstallError(ctx *gin.Context, status int, username, message string) {
	ctx.HTML(status, "installed.html", gin.H{
		"error":    message,
		"username": username,
	})
}
