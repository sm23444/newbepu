<template>
  <div class="snow-page">
    <div class="snow-inner">
      <div class="review-toolbar">
        <a-tabs :active-key="reviewStatus" @change="changeReviewStatus">
          <a-tab-pane key="pending" title="待审核" />
          <a-tab-pane key="resolved" title="复核记录" />
        </a-tabs>
        <a-button :loading="loading" @click="loadReviewList">
          <template #icon><icon-refresh /></template>
          刷新
        </a-button>
      </div>

      <a-table
        row-key="id"
        size="small"
        :bordered="{ cell: true }"
        :scroll="{ x: 920, y: '100%' }"
        :loading="loading"
        :data="reviewData"
        :pagination="pagination"
        @page-change="changeReviewPage"
        @page-size-change="changeReviewPageSize"
      >
        <template #columns>
          <a-table-column title="商户订单" data-index="order_id" :width="180" ellipsis tooltip />
          <a-table-column title="交易类型" data-index="trade_type" :width="130" />
          <a-table-column title="交易编号" data-index="transaction_hash" :width="210" ellipsis tooltip />
          <a-table-column title="状态" data-index="status" :width="100">
            <template #cell="{ record }">
              <a-tag :color="reviewStatusColor(record.status)">{{ reviewStatusText(record.status) }}</a-tag>
            </template>
          </a-table-column>
          <a-table-column :title="reviewStatus === 'pending' ? '申请时间' : '处理时间'" :width="180">
            <template #cell="{ record }">{{ reviewStatus === "pending" ? record.created_at : record.reviewed_at || "-" }}</template>
          </a-table-column>
          <a-table-column title="操作" :width="90" fixed="right">
            <template #cell="{ record }">
              <a-button size="mini" type="primary" @click="openReviewDetail(record.id)">查看</a-button>
            </template>
          </a-table-column>
        </template>
      </a-table>

      <a-empty
        v-if="!loading && reviewData.length === 0"
        :description="reviewStatus === 'pending' ? '暂无待审核复核' : '暂无复核记录'"
      />
    </div>
  </div>

  <a-modal v-model:visible="detailVisible" title="付款复核详情" :width="dialogWidth('760px')" :footer="false" unmount-on-close>
    <a-descriptions v-if="reviewDetail" :column="1" bordered size="small">
      <a-descriptions-item label="商户订单">{{ reviewDetail.order_id }}</a-descriptions-item>
      <a-descriptions-item label="交易类型">{{ reviewDetail.trade_type }}</a-descriptions-item>
      <a-descriptions-item label="交易编号">{{ reviewDetail.transaction_hash || "未填写" }}</a-descriptions-item>
      <a-descriptions-item label="付款说明">{{ reviewDetail.description }}</a-descriptions-item>
      <a-descriptions-item label="金额">
        {{ reviewDetail.order_amount }} {{ reviewDetail.order_crypto }} / {{ reviewDetail.order_money }} {{ reviewDetail.order_fiat }}
      </a-descriptions-item>
      <a-descriptions-item label="复核状态">
        <a-tag :color="reviewStatusColor(reviewDetail.status)">{{ reviewStatusText(reviewDetail.status) }}</a-tag>
      </a-descriptions-item>
      <a-descriptions-item v-if="reviewDetail.status !== 'pending'" label="审核备注">
        {{ reviewDetail.resolution_note || "-" }}
      </a-descriptions-item>
      <a-descriptions-item v-if="reviewDetail.status !== 'pending'" label="审核人">
        {{ reviewerText(reviewDetail.reviewed_by) }}
      </a-descriptions-item>
      <a-descriptions-item v-if="reviewDetail.status !== 'pending'" label="处理时间">
        {{ reviewDetail.reviewed_at || "-" }}
      </a-descriptions-item>
    </a-descriptions>

    <img v-if="reviewDetail?.evidence_data_url" :src="reviewDetail.evidence_data_url" alt="付款凭证" class="review-evidence" />

    <a-form v-if="reviewDetail?.status === 'pending'" :model="reviewForm" layout="vertical" class="review-form">
      <a-form-item label="交易编号（可补录）">
        <a-input v-model="reviewForm.transaction_hash" placeholder="OKX 填 Bill ID，链上填交易哈希" allow-clear />
      </a-form-item>
      <a-form-item label="审核备注">
        <a-textarea v-model="reviewForm.note" :max-length="1000" placeholder="请填写审核说明" />
      </a-form-item>
      <a-space wrap>
        <a-button status="danger" :loading="resolving" @click="resolveReview('reject')">拒绝</a-button>
        <a-button type="primary" :loading="resolving" @click="resolveReview('approve')">人工核实后批准入账</a-button>
      </a-space>
    </a-form>
  </a-modal>
</template>

<script setup lang="ts">
import { Notification } from "@arco-design/web-vue";
import { reviewDetailAPI, reviewListAPI, reviewResolveAPI } from "@/api/modules/review/index";
import { useLayoutModel } from "@/hooks/useLayoutModel";

const { dialogWidth } = useLayoutModel();
const reviewStatus = ref("pending");
const loading = ref(false);
const resolving = ref(false);
const detailVisible = ref(false);
const reviewData = reactive<any[]>([]);
const reviewDetail = ref<any>(null);
const reviewForm = reactive({ transaction_hash: "", note: "" });
const pagination = ref({ showPageSize: true, showTotal: true, current: 1, pageSize: 20, total: 0 });

const reviewStatusText = (status: string) => ({ pending: "待审核", approved: "已通过", rejected: "已拒绝" })[status] || status;
const reviewStatusColor = (status: string) => (status === "pending" ? "orange" : status === "approved" ? "green" : "gray");
const reviewerText = (reviewer: string) => (reviewer === "system" ? "系统" : reviewer || "-");

const loadReviewList = async () => {
  loading.value = true;
  try {
    const res = await reviewListAPI({
      page: pagination.value.current,
      size: pagination.value.pageSize,
      status: reviewStatus.value
    });
    reviewData.length = 0;
    reviewData.push(...(res.data || []));
    pagination.value.total = Number(res.total || 0);
  } catch (error) {
    Notification.error(error);
  } finally {
    loading.value = false;
  }
};

const changeReviewStatus = async (status: string | number) => {
  reviewStatus.value = String(status);
  pagination.value.current = 1;
  await loadReviewList();
};

const changeReviewPage = async (page: number) => {
  pagination.value.current = page;
  await loadReviewList();
};

const changeReviewPageSize = async (pageSize: number) => {
  pagination.value.current = 1;
  pagination.value.pageSize = pageSize;
  await loadReviewList();
};

const openReviewDetail = async (id: number) => {
  try {
    const res = await reviewDetailAPI({ id });
    reviewDetail.value = res.data;
    reviewForm.transaction_hash = res.data.transaction_hash || "";
    reviewForm.note = "";
    detailVisible.value = true;
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

  resolving.value = true;
  try {
    await reviewResolveAPI({
      id: reviewDetail.value.id,
      decision,
      transaction_hash: reviewForm.transaction_hash.trim(),
      note: reviewForm.note.trim()
    });
    Notification.success(decision === "approve" ? "复核通过并已入账" : "复核已拒绝");
    detailVisible.value = false;
    reviewDetail.value = null;
    await loadReviewList();
  } catch (error) {
    Notification.error(error);
  } finally {
    resolving.value = false;
  }
};

loadReviewList();
</script>

<style lang="scss" scoped>
.review-toolbar {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 8px;
}

.review-toolbar :deep(.arco-tabs) {
  flex: 1;
  min-width: 0;
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

.review-form {
  margin-top: 16px;
}

@media (max-width: 768px) {
  .review-toolbar {
    align-items: stretch;
    flex-direction: column;
  }
}
</style>
