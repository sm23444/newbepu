package model

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/spf13/cast"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

var confCache sync.Map
var installMu sync.Mutex
var installTokenHash [sha256.Size]byte
var installTokenReady bool

var (
	ErrAlreadyInstalled     = errors.New("system is already installed")
	ErrInvalidInstallToken  = errors.New("invalid install token")
	ErrInvalidInstallUser   = errors.New("invalid administrator username")
	ErrInvalidInstallPasswd = errors.New("invalid administrator password")
)

var defaultConf = map[ConfKey]string{
	ApiAppUri:               "",
	RateSyncInterval:        "3600",
	AtomUSDT:                "0.01",
	AtomUSDC:                "0.01",
	AtomTRX:                 "0.01",
	AtomBNB:                 "0.00001",
	AtomETH:                 "0.000001",
	AtomGRAM:                "0.01",
	AtomExchangeUSDT:        "0.0001",
	AtomExchangeUSDC:        "0.0001",
	MonitorMinAmount:        "0.01",
	PaymentMinAmount:        "0.01",
	PaymentMaxAmount:        "99999",
	RpcEndpointTron:         "grpc.trongrid.io:50051",
	RpcEndpointBsc:          "https://binance-smart-chain-public.nodies.app/",
	RpcEndpointSolana:       "https://solana-rpc.publicnode.com/",
	RpcEndpointXlayer:       "https://xlayerrpc.okx.com/",
	RpcEndpointPolygon:      "https://polygon-public.nodies.app/",
	RpcEndpointArbitrum:     "https://arb1.arbitrum.io/rpc",
	RpcEndpointEthereum:     "https://ethereum-public.nodies.app/",
	RpcEndpointBase:         "https://base-public.nodies.app/",
	RpcEndpointAptos:        "https://aptos-rest.publicnode.com/",
	RpcEndpointPlasma:       "https://rpc.plasma.to/",
	RpcGlobalConfigUrlTon:   "https://ton.org/global-config.json",
	NotifyMaxRetry:          "10",
	BlockHeightMaxDiff:      "1000",
	BlockOffsetConfirm:      "0",
	PaymentTimeout:          "1200", // 20分钟
	PaymentCheckout:         "sm",   // SM 模板
	PaymentMatchMode:        string(Classic),
	PaymentSupportUrl:       "",
	PaymentLookbackHour:     "3",
	PaymentNetworkSort:      "",
	ExchangePollInterval:    "",
	ExchangeTimeout:         "",
	ExchangeBinanceEnabled:  "",
	ExchangeBinanceAPIURL:   "",
	ExchangeOKXEnabled:      "",
	ExchangeOKXAPIURL:       "",
	SystemInstallLock:       "0",
	RateSyncCoingeckoApiUrl: "https://api.coingecko.com",
	RateSyncHistoryDays:     "30",
	MqttTopicPrefix:         "bepusdt",
	HomeRedirectUrl:         "",
}

type Conf struct {
	K ConfKey `gorm:"column:k;type:varchar(32);not null;primaryKey" json:"key"`
	V string  `gorm:"column:v;type:varchar(512);not null" json:"val"`
}

func (c Conf) TableName() string {

	return "bep_conf"
}

func SetK(k ConfKey, v string) error {
	if Db == nil {
		return errors.New("configuration database is not initialized")
	}

	if err := Db.Transaction(func(db *gorm.DB) error {
		if err2 := db.Where("k = ?", k).Delete(&Conf{}).Error; err2 != nil {

			return err2
		}
		if err2 := db.Create(&Conf{K: k, V: v}).Error; err2 != nil {

			return err2
		}

		return nil
	}); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, fmt.Sprintf("设置配置项 %s 错误：%s", k, err.Error()))
		return err
	}

	return RefreshC()
}

func GetK(k ConfKey) string {
	var row Conf

	var tx = Db.Where("k = ?", k).Limit(1).Find(&row)
	if tx.Error == nil {

		return row.V
	}

	_, _ = fmt.Fprintln(os.Stderr, fmt.Sprintf("获取配置项 %s 错误：%s", k, tx.Error.Error()))

	return ""
}

func GetVs(keys []ConfKey) map[ConfKey]string {
	var rows = make([]Conf, 0)
	Db.Where("k IN ?", keys).Find(&rows)

	var result = make(map[ConfKey]string)
	for _, row := range rows {
		result[row.K] = row.V
	}

	for _, k := range keys {
		if _, ok := result[k]; !ok {
			result[k] = ""
		}
	}

	return result
}

// GetC 从缓存获取配置，适用于高频读取，依赖 RefreshC 刷新缓存
func GetC(k ConfKey) string {
	value, ok := confCache.Load(k)
	if !ok {
		return ""
	}

	valueString, ok := value.(string)
	if !ok {
		return ""
	}

	return valueString
}

func RefreshC() error {
	if Db == nil {
		return errors.New("configuration database is not initialized")
	}

	var rows = make([]Conf, 0)
	if err := Db.Find(&rows).Error; err != nil {
		return err
	}

	confCache.Clear()
	for _, row := range rows {
		confCache.Store(row.K, row.V)
	}

	return nil
}

func CheckoutUrl(host, id string) string {
	uri := GetK(ApiAppUri)
	if uri == "" {
		uri = host
	}

	return fmt.Sprintf("%s/pay/checkout/%s", uri, id)
}

func randomBytes(size int) ([]byte, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return nil, err
	}

	return value, nil
}

func randomHex(size int) (string, error) {
	value, err := randomBytes(size)
	if err != nil {
		return "", err
	}
	defer clear(value)

	return strings.ToUpper(hex.EncodeToString(value)), nil
}

func ConfInit() error {
	password, err := randomBytes(32)
	if err != nil {
		return fmt.Errorf("generate initial password: %w", err)
	}
	defer clear(password)

	encrypt, err := bcrypt.GenerateFromPassword(password, bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash initial password: %w", err)
	}

	token, err := randomHex(32)
	if err != nil {
		return fmt.Errorf("generate API token: %w", err)
	}

	secret, err := randomHex(32)
	if err != nil {
		return fmt.Errorf("generate session secret: %w", err)
	}

	secureID, err := randomHex(16)
	if err != nil {
		return fmt.Errorf("generate secure entrance: %w", err)
	}

	var data = map[ConfKey]string{
		ApiAuthToken:  token,
		AdminSecret:   secret,
		AdminSecure:   "/" + secureID,
		AdminUsername: "admin",
		AdminPassword: string(encrypt),
	}
	var rows = make([]Conf, 0)
	for k, v := range data {
		rows = append(rows, Conf{K: k, V: v})
	}
	for k, v := range defaultConf {
		rows = append(rows, Conf{K: k, V: v})
	}

	if err := Db.Create(&rows).Error; err != nil {
		return fmt.Errorf("persist initial configuration: %w", err)
	}

	return nil
}

func AuthToken() string {

	return GetK(ApiAuthToken)
}

func IsInstalled() bool {
	return GetC(SystemInstallLock) == "1"
}

func prepareInstallToken() error {
	installMu.Lock()
	defer installMu.Unlock()

	if IsInstalled() {
		destroyInstallToken()
		return nil
	}

	tokenBytes, err := randomBytes(32)
	if err != nil {
		return fmt.Errorf("generate one-time install token: %w", err)
	}
	token := strings.ToUpper(hex.EncodeToString(tokenBytes))
	clear(tokenBytes)

	installTokenHash = sha256.Sum256([]byte(token))
	installTokenReady = true

	fmt.Println()
	fmt.Println("BEpusdt 尚未完成安全初始化")
	fmt.Printf("一次性安装令牌: %s\n", token)
	fmt.Println("请打开网关首页，使用该令牌设置管理员账号和密码。")
	fmt.Println("初始化成功后令牌立即失效；服务重启会生成新令牌。")
	fmt.Println()

	return nil
}

func destroyInstallToken() {
	clear(installTokenHash[:])
	installTokenReady = false
}

func CompleteInstall(token, username, password string) (string, error) {
	installMu.Lock()
	defer installMu.Unlock()

	if IsInstalled() {
		destroyInstallToken()
		return "", ErrAlreadyInstalled
	}

	candidateHash := sha256.Sum256([]byte(strings.ToUpper(strings.TrimSpace(token))))
	if !installTokenReady || subtle.ConstantTimeCompare(candidateHash[:], installTokenHash[:]) != 1 {
		return "", ErrInvalidInstallToken
	}

	username = strings.TrimSpace(username)
	if username == "" || utf8.RuneCountInString(username) > 32 {
		return "", ErrInvalidInstallUser
	}
	if utf8.RuneCountInString(password) < 6 || len(password) > 72 {
		return "", ErrInvalidInstallPasswd
	}

	encrypt, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash administrator password: %w", err)
	}

	var secure string
	err = Db.Transaction(func(db *gorm.DB) error {
		lock := db.Model(&Conf{}).
			Where("k = ? AND v = ?", SystemInstallLock, "0").
			Update("v", "1")
		if lock.Error != nil {
			return lock.Error
		}
		if lock.RowsAffected != 1 {
			return ErrAlreadyInstalled
		}

		updates := map[ConfKey]string{
			AdminUsername: username,
			AdminPassword: string(encrypt),
		}
		for key, value := range updates {
			result := db.Model(&Conf{}).Where("k = ?", key).Update("v", value)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("missing initial configuration %s", key)
			}
		}

		var row Conf
		if err := db.Where("k = ?", AdminSecure).Take(&row).Error; err != nil {
			return err
		}
		if row.V == "" {
			return errors.New("secure entrance is empty")
		}
		secure = row.V

		return nil
	})
	if err != nil {
		if errors.Is(err, ErrAlreadyInstalled) {
			destroyInstallToken()
		}
		return "", err
	}

	RefreshC()
	destroyInstallToken()

	return secure, nil
}

func GetTronGridApiKeys() []string {
	arr := strings.Split(GetC(RpcEndpointTronGridApiKey), ",")
	keys := make([]string, 0)
	for _, v := range arr {
		if v != "" {
			keys = append(keys, v)
		}
	}

	return keys
}

func FillDefaultConf() {
	var existKeys []string
	Db.Model(&Conf{}).Pluck("k", &existKeys)

	existSet := make(map[ConfKey]struct{}, len(existKeys))
	for _, k := range existKeys {
		existSet[ConfKey(k)] = struct{}{}
	}

	var rows []Conf
	for k, v := range defaultConf {
		if _, ok := existSet[k]; !ok {
			rows = append(rows, Conf{K: k, V: v})
		}
	}
	if len(rows) > 0 {
		Db.Create(&rows)
	}
}

func GetLookbackHour() time.Duration {
	var hour = time.Hour * -1
	var num = cast.ToInt(GetC(PaymentLookbackHour))

	return time.Duration(num) * hour
}
