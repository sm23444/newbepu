package admin

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/v03413/bepusdt/app/conf"
	"github.com/v03413/bepusdt/app/handler/base"
	"github.com/v03413/bepusdt/app/model"
	"github.com/v03413/bepusdt/app/notifier"
	"github.com/v03413/bepusdt/app/utils"
	"gorm.io/gorm"
)

type Conf struct {
}

type confReq struct {
	Key   string `json:"key" binding:"required"`
	Value string `json:"value"`
}

type confGetsReq struct {
	Keys []string `json:"keys" binding:"required"`
}

type confSetsReq []confReq

const maskedConfValue = "[已配置]"

var editableConfKeys = map[model.ConfKey]struct{}{
	model.AdminUsername: {}, model.AdminSecure: {}, model.ApiAppUri: {},
	model.AtomUSDT: {}, model.AtomUSDC: {}, model.AtomTRX: {}, model.AtomBNB: {}, model.AtomETH: {}, model.AtomGRAM: {}, model.AtomExchangeUSDT: {}, model.AtomExchangeUSDC: {},
	model.MonitorMinAmount: {}, model.PaymentMinAmount: {}, model.PaymentMaxAmount: {}, model.PaymentTimeout: {}, model.PaymentCheckout: {}, model.PaymentMatchMode: {}, model.PaymentSupportUrl: {}, model.PaymentLookbackHour: {}, model.PaymentNetworkSort: {},
	model.RpcEndpointPlasma: {}, model.RpcEndpointBsc: {}, model.RpcEndpointSolana: {}, model.RpcEndpointXlayer: {}, model.RpcEndpointPolygon: {}, model.RpcEndpointArbitrum: {}, model.RpcEndpointEthereum: {}, model.RpcEndpointBase: {}, model.RpcEndpointAptos: {}, model.RpcEndpointTron: {}, model.RpcEndpointTronGridApiKey: {}, model.RpcGlobalConfigUrlTon: {},
	model.RateSyncCoingeckoApiUrl: {}, model.RateSyncCoingeckoApiKey: {}, model.RateSyncInterval: {}, model.RateSyncHistoryDays: {},
	model.NotifyMaxRetry: {}, model.BlockHeightMaxDiff: {}, model.BlockOffsetConfirm: {},
	model.MqttHost: {}, model.MqttPort: {}, model.MqttUser: {}, model.MqttPass: {}, model.MqttPublishQos: {}, model.MqttTopicPrefix: {}, model.MqttNetworks: {}, model.HomeRedirectUrl: {},
}

func isEditableConfKey(key model.ConfKey) bool {
	_, ok := editableConfKeys[key]
	return ok
}

func validPublicURLSetting(key model.ConfKey, value string) bool {
	return key != model.PaymentSupportUrl || value == "" || utils.IsAllowedHTTPSURL(value)
}

type notifierConf struct {
	Channel string          `json:"channel" binding:"required"`
	Params  json.RawMessage `json:"params" binding:"required"`
}

func (Conf) Set(ctx *gin.Context) {
	var req confReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		base.BadRequest(ctx, err.Error())

		return
	}

	key := model.ConfKey(strings.TrimSpace(req.Key))
	if !isEditableConfKey(key) {
		base.BadRequest(ctx, "该配置项不能通过通用配置接口修改")
		return
	}
	value := strings.TrimSpace(req.Value)
	value = preserveMaskedConfValue(key, value)
	if !validPublicURLSetting(key, value) {
		base.BadRequest(ctx, "payment_support_url must be a valid HTTPS URL")
		return
	}

	if err := model.SetK(key, value); err != nil {
		base.Error(ctx, err)
		return
	}

	base.Ok(ctx, "配置成功")
}

func (Conf) Get(ctx *gin.Context) {
	var req confReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		base.BadRequest(ctx, err.Error())

		return
	}

	key := model.ConfKey(strings.TrimSpace(req.Key))
	base.Ok(ctx, gin.H{"key": key, "value": safeConfValue(key, model.GetK(key))})
}

func (Conf) Del(ctx *gin.Context) {
	var req confReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		base.BadRequest(ctx, err.Error())

		return
	}

	base.BadRequest(ctx, "通用配置删除接口已禁用")
}

func (Conf) Gets(ctx *gin.Context) {
	var req confGetsReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		base.BadRequest(ctx, err.Error())

		return
	}

	var items = make([]model.Conf, 0)
	if err := model.Db.Where("k IN ?", req.Keys).Find(&items).Error; err != nil {
		base.Error(ctx, err)
		return
	}

	var data = gin.H{}
	for _, item := range items {
		data[string(item.K)] = safeConfValue(item.K, item.V)
	}

	base.Ok(ctx, data)
}

func (Conf) Sets(ctx *gin.Context) {
	var req confSetsReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		base.BadRequest(ctx, err.Error())

		return
	}

	keys := make([]string, 0)
	data := make([]model.Conf, 0)
	for _, item := range req {
		key := model.ConfKey(strings.TrimSpace(item.Key))
		if !isEditableConfKey(key) {
			base.BadRequest(ctx, "该配置项不能通过通用配置接口修改")
			return
		}
		value := strings.TrimSpace(item.Value)
		value = preserveMaskedConfValue(key, value)
		if !validPublicURLSetting(key, value) {
			base.BadRequest(ctx, "payment_support_url must be a valid HTTPS URL")
			return
		}

		keys = append(keys, string(key))
		data = append(data, model.Conf{K: key, V: value})
	}

	if err := model.Db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("k IN ?", keys).Delete(&model.Conf{}).Error; err != nil {
			return err
		}
		return tx.Create(&data).Error
	}); err != nil {
		base.Error(ctx, err)
		return
	}
	if err := model.RefreshC(); err != nil {
		base.Error(ctx, err)
		return
	}

	base.Ok(ctx, "配置成功")
}

func (Conf) Rpc(ctx *gin.Context) {
	var keys = []model.ConfKey{
		model.RpcEndpointPlasma,
		model.RpcEndpointBsc,
		model.RpcEndpointSolana,
		model.RpcEndpointXlayer,
		model.RpcEndpointPolygon,
		model.RpcEndpointArbitrum,
		model.RpcEndpointEthereum,
		model.RpcEndpointBase,
		model.RpcEndpointAptos,
		model.RpcEndpointTron,
		model.RpcEndpointTronGridApiKey,
		model.RpcGlobalConfigUrlTon,
	}

	var rpc = make(map[model.ConfKey]string)
	var items = make([]model.Conf, 0)
	if err := model.Db.Where("k IN ?", keys).Find(&items).Error; err != nil {
		base.Error(ctx, err)
		return
	}
	for _, item := range items {
		rpc[item.K] = safeConfValue(item.K, item.V)
	}

	base.Ok(ctx, gin.H{
		"rpc":   rpc,
		"stats": conf.GetStats(),
	})
}

func safeConfValue(key model.ConfKey, value string) string {
	if model.IsSensitiveConfKey(key) && strings.TrimSpace(value) != "" {
		return maskedConfValue
	}
	return value
}

func preserveMaskedConfValue(key model.ConfKey, value string) string {
	if model.IsSensitiveConfKey(key) && value == maskedConfValue {
		return model.GetK(key)
	}
	return value
}

func (Conf) Notifier(ctx *gin.Context) {
	var req notifierConf
	if err := ctx.ShouldBindJSON(&req); err != nil {
		base.BadRequest(ctx, err.Error())

		return
	}

	var keys = []string{string(model.NotifierChannel), string(model.NotifierParams)}
	if err := model.Db.Transaction(func(tx *gorm.DB) error {
		var existing []model.Conf
		if err := tx.Where("k IN ?", keys).Find(&existing).Error; err != nil {
			return err
		}
		current := make(map[model.ConfKey]string, len(existing))
		for _, item := range existing {
			current[item.K] = item.V
		}
		params := req.Params
		if req.Channel == current[model.NotifierChannel] && current[model.NotifierParams] != "" {
			var submitted map[string]string
			if err := json.Unmarshal(req.Params, &submitted); err != nil {
				return err
			}
			var stored map[string]string
			if err := json.Unmarshal([]byte(current[model.NotifierParams]), &stored); err == nil {
				if stored == nil {
					stored = make(map[string]string)
				}
				for key, value := range submitted {
					value = strings.TrimSpace(value)
					if value != "" && value != maskedConfValue {
						stored[key] = value
					}
				}
				params = mustJSON(stored)
			}
		}
		if err := tx.Where("k IN ?", keys).Delete(&model.Conf{}).Error; err != nil {
			return err
		}
		return tx.Create(&[]model.Conf{
			{K: model.NotifierChannel, V: req.Channel},
			{K: model.NotifierParams, V: string(mustJSON(params))},
		}).Error
	}); err != nil {
		base.Error(ctx, err)
		return
	}
	if err := model.RefreshC(); err != nil {
		base.Error(ctx, err)
		return
	}

	base.Ok(ctx, "配置成功")
}

func mustJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage("{}")
	}
	return json.RawMessage(data)
}

func (Conf) NotifierTest(ctx *gin.Context) {
	err := notifier.Test()
	if err != nil {
		base.Ok(ctx, "发送测试失败："+err.Error())

		return
	}

	base.Ok(ctx, "发送测试成功")
}

func (Conf) CheckoutList(ctx *gin.Context) {
	base.Ok(ctx, model.CheckoutList())
}

func (Conf) ResetApiAuthToken(ctx *gin.Context) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		base.Error(ctx, err)
		return
	}
	token := strings.ToUpper(hex.EncodeToString(tokenBytes))
	clear(tokenBytes)

	if err := model.SetK(model.ApiAuthToken, token); err != nil {
		base.Error(ctx, err)
		return
	}

	base.Ok(ctx, gin.H{"token": token})
}
