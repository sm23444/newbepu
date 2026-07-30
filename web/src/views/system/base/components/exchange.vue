<template>
  <a-spin :loading="loading" class="exchange-settings">
    <section class="settings-panel">
      <div class="panel-title">交易所支付设置</div>
      <div class="panel-body">
        <a-form :model="form" :layout="layoutMode" class="exchange-form">
          <a-form-item label="交易所">
            <a-radio-group v-model="activeProvider" type="button">
              <a-radio value="binance">币安交易所</a-radio>
              <a-radio value="okx">欧易交易所</a-radio>
            </a-radio-group>
          </a-form-item>

          <a-form-item label="启用状态" :extra="`${providerName} 关闭后不会出现在收银台支付方式中`">
            <a-space>
              <a-switch v-model="providerForm.enabled" />
              <a-tag :color="sourceColor(providerForm.source)">{{ sourceLabel(providerForm.source) }}</a-tag>
            </a-space>
          </a-form-item>

          <a-form-item
            :label="providerAccountLabel"
            extra="收款账户 UID，必须与下方 API 凭证所属账户一致"
            required
          >
            <a-input v-model="providerForm.uid" :placeholder="providerAccountLabel" />
          </a-form-item>

          <a-form-item label="API 地址" extra="一般保持默认地址即可" required>
            <a-input v-model="providerForm.api_url" :placeholder="defaultApiUrl" />
          </a-form-item>

          <a-form-item label="API Key" extra="只授予只读权限，并设置当前服务器公网 IP 白名单" required>
            <a-input-password
              v-model="providerForm.api_key"
              :placeholder="credentialPlaceholder('api_key')"
            />
          </a-form-item>

          <a-form-item label="Secret Key" extra="已保存的密钥不会再次显示，留空表示保持不变" required>
            <a-input-password
              v-model="providerForm.secret_key"
              :placeholder="credentialPlaceholder('secret_key')"
            />
          </a-form-item>

          <a-form-item
            v-if="activeProvider === 'okx'"
            label="Passphrase"
            extra="创建 OKX API Key 时设置的密码短语"
            required
          >
            <a-input-password
              v-model="providerForm.passphrase"
              :placeholder="credentialPlaceholder('passphrase')"
            />
          </a-form-item>

          <a-form-item label="轮询间隔" extra="查询交易所入账记录的时间间隔，建议保持 10s">
            <a-input v-model="form.poll_interval" placeholder="10s" />
          </a-form-item>

          <a-form-item label="请求超时" extra="单次查询交易所接口的最长等待时间，建议保持 8s">
            <a-input v-model="form.timeout" placeholder="8s" />
          </a-form-item>

          <a-form-item>
            <a-space>
              <a-button type="primary" :loading="saving" @click="saveConfig">
                <template #icon><icon-save /></template>
                保存配置
              </a-button>
              <a-button
                :loading="testing[activeProvider]"
                :disabled="!providerForm.enabled"
                @click="testProvider(activeProvider)"
              >
                <template #icon><icon-link /></template>
                测试连接
              </a-button>
            </a-space>
          </a-form-item>
        </a-form>
      </div>
    </section>
  </a-spin>
</template>

<script setup lang="ts">
import { Notification } from "@arco-design/web-vue";
import { getExchangeConfigAPI, saveExchangeConfigAPI, testExchangeConfigAPI } from "@/api/modules/exchange";
import { useDevicesSize } from "@/hooks/useDevicesSize";

type Provider = "binance" | "okx";
type ProviderForm = {
  enabled: boolean;
  api_url: string;
  uid: string;
  api_key: string;
  secret_key: string;
  passphrase: string;
  api_key_configured: boolean;
  secret_key_configured: boolean;
  passphrase_configured: boolean;
  source: "database" | "environment" | "none";
};

const emptyProvider = (): ProviderForm => ({
  enabled: false,
  api_url: "",
  uid: "",
  api_key: "",
  secret_key: "",
  passphrase: "",
  api_key_configured: false,
  secret_key_configured: false,
  passphrase_configured: false,
  source: "none"
});

const { isMobile } = useDevicesSize();
const layoutMode = computed(() => (isMobile.value ? "vertical" : "horizontal"));
const activeProvider = ref<Provider>("binance");
const loading = ref(false);
const saving = ref(false);
const testing = reactive<Record<Provider, boolean>>({ binance: false, okx: false });
const form = reactive({
  poll_interval: "10s",
  timeout: "8s",
  binance: emptyProvider(),
  okx: emptyProvider()
});

const providerForm = computed(() => form[activeProvider.value]);
const providerName = computed(() => (activeProvider.value === "binance" ? "币安交易所" : "欧易交易所"));
const providerAccountLabel = computed(() => `${providerName.value} ${activeProvider.value === "binance" ? "ID" : "UID"}`);
const defaultApiUrl = computed(() =>
  activeProvider.value === "binance" ? "https://api-gcp.binance.com" : "https://www.okx.com"
);

const loadConfig = async () => {
  loading.value = true;
  try {
    const response = await getExchangeConfigAPI();
    form.poll_interval = response.data.poll_interval || "10s";
    form.timeout = response.data.timeout || "8s";
    Object.assign(form.binance, emptyProvider(), response.data.binance || {});
    Object.assign(form.okx, emptyProvider(), response.data.okx || {});
  } finally {
    loading.value = false;
  }
};

const saveConfig = async () => {
  saving.value = true;
  try {
    await saveExchangeConfigAPI({
      poll_interval: form.poll_interval,
      timeout: form.timeout,
      binance: form.binance,
      okx: form.okx
    });
    Notification.success("交易所支付配置已保存");
    await loadConfig();
  } finally {
    saving.value = false;
  }
};

const testProvider = async (provider: Provider) => {
  testing[provider] = true;
  try {
    const response = await testExchangeConfigAPI(provider);
    Notification.success(`${provider === "binance" ? "Binance" : "OKX"} 连接成功，24 小时账单 ${response.data.transactions_24h} 条`);
  } finally {
    testing[provider] = false;
  }
};

const credentialPlaceholder = (field: "api_key" | "secret_key" | "passphrase") => {
  const configuredKey = `${field}_configured` as keyof ProviderForm;
  return providerForm.value[configuredKey] ? "已配置，留空保持不变" : `请输入 ${field === "passphrase" ? "Passphrase" : field === "api_key" ? "API Key" : "Secret Key"}`;
};

const sourceLabel = (source: ProviderForm["source"]) => {
  if (source === "database") return "后台配置";
  if (source === "environment") return "环境变量";
  return "未配置";
};

const sourceColor = (source: ProviderForm["source"]) => {
  if (source === "database") return "arcoblue";
  if (source === "environment") return "green";
  return "gray";
};

loadConfig();
</script>

<style scoped lang="scss">
.exchange-settings {
  display: block;
  width: 100%;
}

.settings-panel {
  border: 1px solid var(--color-border-2);
  background: var(--color-bg-2);
}

.panel-title {
  padding: 14px 16px;
  border-bottom: 1px solid var(--color-border-2);
  color: var(--color-text-1);
  font-size: 16px;
  font-weight: 600;
}

.panel-body {
  padding: 16px;
}

.exchange-form {
  width: 100%;
  max-width: 680px;
}

:deep(.arco-form-item-label-col) {
  flex: 0 0 122px;
}

:deep(.arco-form-item-wrapper-col) {
  min-width: 0;
}

:deep(.arco-radio-group-button) {
  min-width: 260px;
}

@media (max-width: 640px) {
  .panel-body {
    padding: 14px;
  }

  :deep(.arco-form-item-label-col) {
    flex-basis: auto;
  }

  :deep(.arco-radio-group-button) {
    width: 100%;
    min-width: 0;
  }
}
</style>
