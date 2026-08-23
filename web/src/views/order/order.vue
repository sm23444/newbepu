<template>
  <div class="snow-page">
    <div class="snow-inner">
      <a-form ref="formRef" auto-label-width :model="formData.form">
        <a-row :gutter="16">
          <a-col :xs="24" :sm="24" :md="12" :lg="12" :xl="6" :xxl="6">
            <a-form-item field="order_id" label="商户订单">
              <a-input v-model="formData.form.order_id" placeholder="请输入商户订单" allow-clear />
            </a-form-item>
          </a-col>
          <a-col :xs="24" :sm="24" :md="12" :lg="12" :xl="6" :xxl="6">
            <a-form-item field="trade_type" label="交易类型">
              <a-select v-model="formData.form.trade_type" placeholder="请选择交易类型" allow-clear allow-search>
                <a-option v-for="item in tradeTypeOptions" :key="item.value" :value="item.value">
                  {{ item.label }}
                </a-option>
              </a-select>
            </a-form-item>
          </a-col>
          <a-col :xs="24" :sm="24" :md="12" :lg="12" :xl="6" :xxl="6">
            <a-form-item field="status" label="订单状态">
              <a-select v-model="formData.form.status" placeholder="请选择订单状态" allow-clear>
                <a-option v-for="item in statusOptions" :key="item.value" :value="item.value">
                  {{ item.label }}
                </a-option>
              </a-select>
            </a-form-item>
          </a-col>

          <a-col :xs="24" :sm="24" :md="12" :lg="12" :xl="6" :xxl="6">
            <a-space class="search-btn" wrap>
              <a-button type="primary" @click="getOrderList">
                <template #icon><icon-search /></template>
                查询
              </a-button>
              <a-button @click="onReset">
                <template #icon><icon-refresh /></template>
                重置
              </a-button>
              <a-button type="primary" status="warning" @click="openReviewModal">
                <template #icon><icon-file /></template>
                付款复核
              </a-button>
              <a-popconfirm :content="batchDelConfirm" type="warning" @ok="onBatchDelete">
                <a-button v-show="selectedKeys.length > 0" type="primary" status="danger">
                  <template #icon><icon-delete /></template>
                  删除
                </a-button>
              </a-popconfirm>
              <a-button type="text" @click="formData.search = !formData.search">
                {{ formData.search ? "收起" : "展开" }}
                <icon-down :class="{ 'rotate-icon': formData.search }" />
              </a-button>
            </a-space>
          </a-col>
        </a-row>
        <a-row :gutter="16" v-if="formData.search">
          <a-col :xs="24" :sm="24" :md="12" :lg="12" :xl="6" :xxl="6">
            <a-form-item field="trade_id" label="交易ID">
              <a-input v-model="formData.form.trade_id" placeholder="请输入交易ID" allow-clear />
            </a-form-item>
          </a-col>
          <a-col :xs="24" :sm="24" :md="12" :lg="12" :xl="6" :xxl="6">
            <a-form-item field="address" label="钱包地址">
              <a-input v-model="formData.form.address" placeholder="请输入钱包地址" allow-clear />
            </a-form-item>
          </a-col>
          <a-col :xs="24" :sm="24" :md="12" :lg="12" :xl="6" :xxl="6">
            <a-form-item field="createTime" label="创建时间">
              <a-range-picker v-model="formData.form.createTime" show-time format="YYYY-MM-DD HH:mm:ss" style="width: 100%" />
            </a-form-item>
          </a-col>
        </a-row>
      </a-form>

      <a-table
        row-key="id"
        size="small"
        :bordered="{ cell: true }"
        :scroll="{ x: '100%', y: '100%', minWidth: 1000 }"
        :loading="loading"
        :columns="columns"
        :data="data"
        v-model:selectedKeys="selectedKeys"
        :row-selection="orderSelection"
        :pagination="pagination"
        @page-change="pageChange"
        @page-size-change="pageSizeChange"
      >
        <template #wallet="{ record }">
          <a-tooltip :content="record.address" position="top">
            <span class="wallet-name">
              {{ record.wallet?.name || record.channel?.name || (record.address ? `${record.address.slice(-8)}` : "--") }}
            </span>
          </a-tooltip>
        </template>

        <template #amount="{ record }">
          <span>
            {{ record.amount }}
            <a-tag size="mini" :color="getCryptoColor(record.crypto)" bordered style="margin-left: 4px">{{
              record.crypto
            }}</a-tag>
          </span>
        </template>

        <template #money="{ record }">
          <span>
            {{ record.money }}
            <a-tag size="mini" color="arcoblue" style="margin-left: 4px">{{ record.fiat }}</a-tag>
          </span>
        </template>

        <template #status="{ record }">
          <a-tag size="small" :color="getStatusColor(record.status)">
            {{ getStatusText(record.status) }}
          </a-tag>
        </template>

        <template #notify_state="{ record }">
          <a-tag size="small" :color="record.status === 2 ? (record.notify_state === 1 ? 'blue' : 'red') : 'gray'">
            {{ record.status === 2 ? (record.notify_state === 1 ? "成功" : "失败") : "-" }}
          </a-tag>
        </template>

        <!-- 不常用的操作优先放置在详情页，尽量保持第一视角的干净整洁 -->
        <template #optional="{ record }">
          <a-space wrap>
            <a-button size="mini" type="primary" @click="showDetail(record)">详情</a-button>
            <a-button size="mini" type="primary" status="warning" :disabled="!canManualPaid(record)" @click="showPaidModal(record)">
              补单
            </a-button>
          </a-space>
        </template>
      </a-table>
    </div>
  </div>

  <DetailModal :visible="detailVisible" :detailData="detailData" @close="closeDetail" @refresh="getOrderList" />

  <!-- 补单弹窗 -->
  <a-modal
    v-model:visible="paidModalVisible"
    title="确认补单操作"
    @ok="confirmPaid"
    @cancel="closePaidModal"
    ok-text="确认补单"
    cancel-text="取消"
    :width="paidDialogWidth"
    :mask-closable="false"
  >
    <div class="paid-modal-content">
      <a-alert type="warning" style="margin-bottom: 20px">
        <template #icon>
          <icon-exclamation-circle-fill />
        </template>
        <div style="font-weight: 500">注意</div>
        <div style="font-size: 13px; margin-top: 4px; color: #666">
          补单操作将强制标记订单为已支付，即使用户实际未付款、谨慎操作!
        </div>
      </a-alert>

      <a-form :model="paidForm" layout="vertical">
        <a-form-item field="ref_hash" label="交易哈希" :rules="[{ maxLength: 200, message: '哈希值不能超过200个字符' }]">
          <a-input v-model="paidForm.ref_hash" placeholder="请输入区块链交易哈希值(可选)" allow-clear>
            <template #prefix>
              <icon-link />
            </template>
          </a-input>
          <template #extra>
            <div style="font-size: 12px; color: #86909c; margin-top: 4px">如有实际交易,建议填写对应的区块链交易哈希值</div>
          </template>
        </a-form-item>
      </a-form>
    </div>
  </a-modal>

  <a-modal v-model:visible="reviewModalVisible" title="付款复核" :width="reviewDialogWidth" :footer="false" unmount-on-close>
    <a-tabs :active-key="reviewStatus" @change="changeReviewStatus">
      <a-tab-pane key="pending" title="待审核" />
      <a-tab-pane key="resolved" title="复核记录" />
    </a-tabs>
    <a-table :data="reviewData" :pagination="false" :loading="reviewLoading" row-key="id" size="small" :scroll="{ x: 720 }">
      <template #columns>
        <a-table-column title="订单" data-index="order_id" ellipsis tooltip />
        <a-table-column title="交易类型" data-index="trade_type" />
        <a-table-column title="交易编号" data-index="transaction_hash" ellipsis tooltip />
        <a-table-column title="状态" data-index="status">
          <template #cell="{ record }">
            <a-tag :color="reviewStatusColor(record.status)">{{ reviewStatusText(record.status) }}</a-tag>
          </template>
        </a-table-column>
        <a-table-column :title="reviewStatus === 'pending' ? '申请时间' : '处理时间'">
          <template #cell="{ record }">{{ reviewStatus === "pending" ? record.created_at : record.reviewed_at || "-" }}</template>
        </a-table-column>
        <a-table-column title="操作">
          <template #cell="{ record }"><a-button size="mini" type="primary" @click="openReviewDetail(record.id)">查看</a-button></template>
        </a-table-column>
      </template>
    </a-table>
    <a-empty
      v-if="!reviewLoading && reviewData.length === 0"
      :description="reviewStatus === 'pending' ? '暂无待审核复核' : '暂无复核记录'"
    />
    <a-pagination
      v-if="reviewTotal > reviewPageSize"
      class="review-pagination"
      :current="reviewPage"
      :page-size="reviewPageSize"
      :total="reviewTotal"
      show-total
      @change="changeReviewPage"
    />
  </a-modal>

  <a-modal v-model:visible="reviewDetailVisible" title="付款复核详情" :width="reviewDialogWidth" :footer="false" unmount-on-close>
    <a-descriptions v-if="reviewDetail" :column="1" bordered size="small">
      <a-descriptions-item label="商户订单">{{ reviewDetail.order_id }}</a-descriptions-item>
      <a-descriptions-item label="交易类型">{{ reviewDetail.trade_type }}</a-descriptions-item>
      <a-descriptions-item label="交易编号">{{ reviewDetail.transaction_hash || "未填写" }}</a-descriptions-item>
      <a-descriptions-item label="付款说明">{{ reviewDetail.description }}</a-descriptions-item>
      <a-descriptions-item label="金额">{{ reviewDetail.order_amount }} {{ reviewDetail.order_crypto }} / {{ reviewDetail.order_money }} {{ reviewDetail.order_fiat }}</a-descriptions-item>
      <a-descriptions-item label="复核状态">
        <a-tag :color="reviewStatusColor(reviewDetail.status)">{{ reviewStatusText(reviewDetail.status) }}</a-tag>
      </a-descriptions-item>
      <a-descriptions-item v-if="reviewDetail.status !== 'pending'" label="审核备注">{{ reviewDetail.resolution_note || "-" }}</a-descriptions-item>
      <a-descriptions-item v-if="reviewDetail.status !== 'pending'" label="审核人">{{ reviewerText(reviewDetail.reviewed_by) }}</a-descriptions-item>
      <a-descriptions-item v-if="reviewDetail.status !== 'pending'" label="处理时间">{{ reviewDetail.reviewed_at || "-" }}</a-descriptions-item>
    </a-descriptions>
    <img v-if="reviewDetail?.evidence_data_url" :src="reviewDetail.evidence_data_url" alt="付款凭证" class="review-evidence" />
    <a-form v-if="reviewDetail?.status === 'pending'" :model="reviewForm" layout="vertical" style="margin-top: 16px">
      <a-form-item label="交易编号（可补录）">
        <a-input v-model="reviewForm.transaction_hash" placeholder="OKX 填 Bill ID，链上填交易哈希" allow-clear />
      </a-form-item>
      <a-form-item label="审核备注">
        <a-textarea v-model="reviewForm.note" :max-length="1000" placeholder="请填写审核说明" />
      </a-form-item>
      <a-space>
        <a-button status="danger" :loading="reviewResolving" @click="resolveReview('reject')">拒绝</a-button>
        <a-button type="primary" :loading="reviewResolving" @click="resolveReview('approve')">人工核实后批准入账</a-button>
      </a-space>
    </a-form>
  </a-modal>
</template>

<script setup lang="ts">
import { listAPI, paidAPI, delOrderApi, reviewListAPI, reviewDetailAPI, reviewResolveAPI } from "@/api/modules/order/index";
import { List, FormData, Pagination } from "./config";
import { Notification } from "@arco-design/web-vue";
import { useUserInfoStore } from "@/store/modules/user-info";
import DetailModal from "./components/detail.vue";
import { useOrderDetail } from "./detail";
import { getCryptoColor } from "@/views/rate/common";
import { useLayoutModel } from "@/hooks/useLayoutModel";

const userStores = useUserInfoStore();
const { detailVisible, detailData, showDetail, closeDetail } = useOrderDetail();
const { dialogWidth } = useLayoutModel();
const paidDialogWidth = computed(() => dialogWidth("500px"));
const reviewDialogWidth = computed(() => dialogWidth("760px"));
const tradeTypeOptions = computed(() => Object.entries(userStores.trade_type).map(([value, label]) => ({ value, label })));

const reviewModalVisible = ref(false);
const reviewDetailVisible = ref(false);
const reviewLoading = ref(false);
const reviewResolving = ref(false);
const reviewData = reactive<any[]>([]);
const reviewDetail = ref<any>(null);
const reviewForm = reactive({ transaction_hash: "", note: "" });
const reviewStatus = ref("pending");
const reviewPage = ref(1);
const reviewPageSize = 20;
const reviewTotal = ref(0);

const reviewStatusText = (status: string) => ({ pending: "待审核", approved: "已通过", rejected: "已拒绝" })[status] || status;
const reviewStatusColor = (status: string) => (status === "pending" ? "orange" : status === "approved" ? "green" : "gray");
const reviewerText = (reviewer: string) => (reviewer === "system" ? "系统" : reviewer || "-");

const loadReviewList = async () => {
  reviewLoading.value = true;
  try {
    const res = await reviewListAPI({ page: reviewPage.value, size: reviewPageSize, status: reviewStatus.value });
    reviewData.length = 0;
    reviewData.push(...(res.data || []));
    reviewTotal.value = Number(res.total || 0);
  } catch (error) {
    Notification.error(error);
  } finally {
    reviewLoading.value = false;
  }
};

const openReviewModal = async () => {
  reviewModalVisible.value = true;
  reviewStatus.value = "pending";
  reviewPage.value = 1;
  await loadReviewList();
};

const changeReviewStatus = async (status: string | number) => {
  reviewStatus.value = String(status);
  reviewPage.value = 1;
  await loadReviewList();
};

const changeReviewPage = async (page: number) => {
  reviewPage.value = page;
  await loadReviewList();
};

const openReviewDetail = async (id: number) => {
  try {
    const res = await reviewDetailAPI({ id });
    reviewDetail.value = res.data;
    reviewForm.transaction_hash = res.data.transaction_hash || "";
    reviewForm.note = "";
    reviewDetailVisible.value = true;
  } catch (error) {
    Notification.error(error);
  }
};

const resolveReview = async (decision: "approve" | "reject") => {
  if (!reviewDetail.value || reviewForm.note.trim().length < 3) {
    Notification.warning("请填写至少3个字的审核备注");
    return;
  }
  if (decision === "approve" && !reviewForm.transaction_hash.trim()) {
    Notification.warning("批准时必须有交易编号或交易哈希");
    return;
  }
  reviewResolving.value = true;
  try {
    await reviewResolveAPI({
      id: reviewDetail.value.id,
      decision,
      transaction_hash: reviewForm.transaction_hash.trim(),
      note: reviewForm.note.trim()
    });
    Notification.success(decision === "approve" ? "复核通过并已入账" : "复核已拒绝");
    reviewDetailVisible.value = false;
    reviewDetail.value = null;
    await loadReviewList();
    await getOrderList();
  } catch (error) {
    Notification.error(error);
  } finally {
    reviewResolving.value = false;
  }
};

const statusOptions = [
  { value: 1, label: "等待支付" },
  { value: 2, label: "支付成功" },
  { value: 3, label: "交易过期" },
  { value: 4, label: "交易取消" },
  { value: 5, label: "等待确认" },
  { value: 6, label: "确认失败" }
];

const formData = reactive<FormData>({
  form: {
    order_id: "",
    trade_id: "",
    trade_type: "",
    address: "",
    status: undefined,
    createTime: []
  },
  search: false
});
const selectedKeys = ref<string[]>([]);
const orderSelection = reactive({
  type: "checkbox",
  showCheckedAll: true,
  onlyCurrent: false
});
const batchDelConfirm = computed(() => `确定删除这${selectedKeys.value.length}条数据吗？`);
const loading = ref(false);
const data = reactive<List[]>([]);
const pagination = ref<Pagination>({
  showPageSize: true,
  showTotal: true,
  current: 1,
  pageSize: 10,
  total: 10
});

const columns = [
  { title: "ID", align: "center", dataIndex: "id", width: 80 },
  { title: "商户订单", align: "center", dataIndex: "order_id", width: 220, ellipsis: true, tooltip: true },
  { title: "交易类型", align: "center", dataIndex: "trade_type", width: 120 },
  { title: "交易数额", align: "center", dataIndex: "amount", slotName: "amount", width: 150 },
  { title: "交易金额", align: "center", dataIndex: "money", slotName: "money", width: 150 },
  { title: "收款钱包", align: "center", dataIndex: "wallet.name", slotName: "wallet", width: 150, ellipsis: true },
  { title: "交易状态", dataIndex: "status", align: "center", slotName: "status", width: 100 },
  { title: "回调", dataIndex: "notify_state", align: "center", slotName: "notify_state", width: 80 },
  { title: "创建时间", dataIndex: "created_at", align: "center", width: 160 },
  { title: "操作", slotName: "optional", align: "center", fixed: "right", width: 150 }
];

const statusMap: Record<number, { color: string; text: string }> = {
  1: { color: "blue", text: "等待支付" },
  2: { color: "green", text: "交易成功" },
  3: { color: "gray", text: "交易过期" },
  4: { color: "gold", text: "交易取消" },
  5: { color: "pinkpurple", text: "等待确认" },
  6: { color: "red", text: "确认失败" }
};

const getStatusColor = (status: number): string => statusMap[status]?.color || "gray";
const getStatusText = (status: number): string => statusMap[status]?.text || "未知";

const pageChange = (page: number) => {
  pagination.value.current = page;
  getOrderList();
};

const pageSizeChange = (pageSize: number) => {
  pagination.value.pageSize = pageSize;
  getOrderList();
};

const onReset = () => {
  formData.form = {
    order_id: "",
    trade_id: "",
    trade_type: "",
    address: "",
    status: undefined,
    createTime: []
  };
  getOrderList();
};

const getOrderList = async () => {
  try {
    loading.value = true;

    const params: any = {
      page: pagination.value.current,
      size: pagination.value.pageSize,
      sort: "desc",
      keyword: "",
      order_id: formData.form.order_id,
      trade_id: formData.form.trade_id,
      address: formData.form.address,
      trade_type: formData.form.trade_type
    };

    // 添加状态筛选
    if (formData.form.status !== undefined) {
      params.status = formData.form.status;
    }

    // 添加时间范围筛选
    if (formData.form.createTime && formData.form.createTime.length === 2) {
      params.start_at = formData.form.createTime[0];
      params.end_at = formData.form.createTime[1];
    }

    const res = await listAPI(params);

    data.length = 0;
    data.push(...res.data);
    pagination.value.total = res.total;
  } finally {
    loading.value = false;
  }
};

const paidModalVisible = ref(false);
const paidForm = reactive({
  ref_hash: "",
  recordId: 0
});

const canManualPaid = (record: List) => record.status !== 2 && record.status !== 4;

const showPaidModal = (record: List) => {
  if (!canManualPaid(record)) return;

  paidForm.recordId = record.id;
  paidForm.ref_hash = "";
  paidModalVisible.value = true;
};

const closePaidModal = () => {
  paidModalVisible.value = false;
  paidForm.ref_hash = "";
  paidForm.recordId = 0;
};

const confirmPaid = async () => {
  try {
    await paidAPI({
      id: paidForm.recordId,
      ref_hash: paidForm.ref_hash || "" // 确保空时传递空字符串
    });
    closePaidModal();
    getOrderList();
    Notification.success("补单成功");
  } catch (error) {
    Notification.error(error);
  }
};
const onBatchDelete = async () => {
  try {
    await delOrderApi({ ids: selectedKeys.value });
    pagination.value.current = 1;
    getOrderList();
    Notification.success("删除成功");
    selectedKeys.value = [];
  } catch (error) {
    Notification.error(error);
  }
};

getOrderList();
</script>

<style lang="scss" scoped>
.rotate-icon {
  transform: rotate(180deg);
  transition: transform 0.3s;
}

.search-btn {
  margin-bottom: 20px;
}

.wallet-name {
  cursor: help;
  color: $color-link;

  &:hover {
    text-decoration: underline;
  }
}

// 在 style 标签中添加
.paid-modal-content {
  padding: 4px 0;

  :deep(.arco-alert) {
    border-radius: 6px;
  }

  :deep(.arco-form-item-label-col) {
    font-weight: 500;
    color: $color-text-1;
  }

  :deep(.arco-input-wrapper) {
    &:hover {
      border-color: $color-primary;
    }
  }
}

.review-evidence {
  display: block;
  max-width: 100%;
  max-height: 360px;
  margin: 16px auto 0;
  object-fit: contain;
  border: 1px solid var(--color-neutral-3);
  border-radius: 6px;
}

.review-pagination {
  display: flex;
  justify-content: flex-end;
  margin-top: 16px;
}

:deep(.arco-modal) {
  .arco-modal-header {
    border-bottom: 1px solid var(--color-neutral-3);
    padding: 16px 20px;
  }

  .arco-modal-body {
    padding: 20px;
  }

  .arco-modal-footer {
    border-top: 1px solid var(--color-neutral-3);
    padding: 12px 20px;
  }
}

// 响应式处理
@media (max-width: 1200px) {
  :deep(.arco-table-th),
  :deep(.arco-table-td) {
    padding: 8px 6px !important;
    font-size: 12px;
  }
}

@media (max-width: 768px) {
  :deep(.arco-modal) {
    width: 95vw !important;
    margin: 10px;
  }

  :deep(.arco-table-th),
  :deep(.arco-table-td) {
    padding: 6px 4px !important;
    font-size: 11px;
  }
}
</style>
