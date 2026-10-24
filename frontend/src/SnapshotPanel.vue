<script setup lang="ts">
import { ref, computed, watch, onUnmounted } from "vue";
import {
  NModal,
  NSelect,
  NInput,
  NInputNumber,
  NCheckbox,
  NSpin,
} from "naive-ui";
import { Call } from "@wailsio/runtime";
import * as Connections from "../bindings/github.com/pp/db-compare/internal/service/connectionservice";
import { message, confirmBox } from "./ui";
import SQLCode from "./SQLCode.vue";
const props = defineProps<{
  show: boolean;
  connections: any[];
  disabled: boolean;
}>();
const emit = defineEmits(["update:show", "restore"]);
const api = (method: string, ...args: any[]) =>
  Call.ByName(
    `github.com/pp/db-compare/internal/service.SnapshotService.${method}`,
    ...args,
  ) as Promise<any>;
const tab = ref("library"),
  busy = ref(false),
  overview = ref<any>({
    snapshots: [],
    tasks: [],
    runs: [],
    background: false,
  });
const group = ref(""),
  filter = ref(""),
  selected = ref<string[]>([]),
  document = ref<any>(null),
  diff = ref<any>(null),
  diffTable = ref(""),
  note = ref(""),
  pinned = ref(false);
const edit = ref<any>(null),
  captureOnly = ref(false),
  pickConnection = ref<string | null>(null),
  pickDatabases = ref<string[]>([]),
  databases = ref<string[]>([]),
  dbLoading = ref(false),
  liveConnection = ref<string | null>(null),
  liveDatabase = ref<string | null>(null),
  liveDatabases = ref<string[]>([]);
const options = ref({
  stripIntDisplayWidth: true,
  unifyUtf8Aliases: true,
  unifyUtf8mb4CiCollations: true,
  ignoreAutoIncrement: true,
  ignoreIndexOrder: true,
  ignoreIndexes: false,
  ignoreCommentsOnly: false,
  compareRowFormat: true,
  hideGipk: true,
});
const fileInput = ref<HTMLInputElement | null>(null);
const connectionOptions = computed(() =>
  props.connections.map((c) => ({ label: c.name || c.host, value: c.id })),
);
const key = (s: any) => JSON.stringify([s.connectionId, s.database]);
const groups = computed(() => {
  const m = new Map<string, any>();
  for (const s of overview.value.snapshots) {
    const k = key(s);
    if (!m.has(k))
      m.set(k, {
        key: k,
        label: s.connectionName,
        database: s.database,
        count: 0,
      });
    m.get(k).count++;
  }
  return [...m.values()];
});
const records = computed(() =>
  overview.value.snapshots.filter(
    (s: any) =>
      (!group.value || key(s) === group.value) &&
      `${s.database} ${s.connectionName} ${s.note}`
        .toLowerCase()
        .includes(filter.value.toLowerCase()),
  ).sort((a: any, b: any) =>
    new Date(b.lastCheckedAt || b.capturedAt).getTime() -
    new Date(a.lastCheckedAt || a.capturedAt).getTime(),
  ),
);
const activeDifference = computed(() =>
  diff.value?.differences.find((d: any) => d.table === diffTable.value),
);
const time = (s: string) =>
  !s ? "—" : s === "当前" ? s : new Date(s).toLocaleString();
const status = (s: string) =>
  (
    ({
      success: "成功",
      partial: "部分失败",
      failed: "失败",
      running: "执行中或已中断",
 interrupted:"已中断",
    }) as any
  )[s] || s;
const targetLabel = (t: any) =>
  `${props.connections.find((c) => c.id === t.connectionId)?.name || "连接已删除"} / ${t.database}`;
const taskSchedule = (t: any) =>
  t.schedule === "interval"
    ? `每 ${t.intervalHours} 小时`
    : t.schedule === "daily"
      ? `每天 ${t.time}`
      : `每周 ${(t.weekdays || []).map((d: number) => "日一二三四五六"[d]).join("、")} ${t.time}`;
async function perform(fn: () => Promise<any>) {
  if (busy.value || props.disabled) return;
  busy.value = true;
  try {
    await fn();
  } catch (e) {
    message.error(String(e));
  } finally {
    busy.value = false;
  }
}
async function refresh() {
  overview.value = await api("Overview");
  if (document.value) {
    const latest = overview.value.snapshots.find(
      (s: any) => s.id === document.value.record.id,
    );
    if (latest) document.value.record = latest;
  }
  selected.value = selected.value.filter((id) =>
    overview.value.snapshots.some((s: any) => s.id === id),
  );
}
let timer: ReturnType<typeof setInterval> | undefined;
watch(
  () => props.show,
  async (show) => {
    if (timer) clearInterval(timer);
    if (show) {
      await perform(refresh);
      timer = setInterval(() => {
        if (!busy.value) void refresh().catch(() => {});
      }, 15000);
    }
  },
);
onUnmounted(() => {
  if (timer) clearInterval(timer);
});
watch(pickConnection, async (id) => {
  pickDatabases.value = [];
  databases.value = [];
  if (!id) return;
  dbLoading.value = true;
  try {
    databases.value =
      (await Connections.ListDatabases(
        props.connections.find((c) => c.id === id),
      )) || [];
  } catch (e) {
    message.error(String(e));
  } finally {
    dbLoading.value = false;
  }
});
watch(liveConnection, async (id) => {
  liveDatabase.value = null;
  liveDatabases.value = [];
  if (!id) return;
  try {
    liveDatabases.value =
      (await Connections.ListDatabases(
        props.connections.find((c) => c.id === id),
      )) || [];
  } catch (e) {
    message.error(String(e));
  }
});
function choose(id: string, checked: boolean) {
  selected.value = checked
    ? [...selected.value, id]
    : selected.value.filter((x) => x !== id);
  diff.value = null;
}
function openRecord(s: any) {
  void perform(async () => {
    document.value = await api("Get", s.id);
    note.value = s.note;
    pinned.value = s.pinned;
    diff.value = null;
    // Browsing archived structure is entirely offline. Connect only on explicit selection.
    liveConnection.value = null;
    liveDatabase.value = null;
    liveDatabases.value = [];
  });
}
function newEdit(task?: any, manual = false) {
  captureOnly.value = manual;
  edit.value = task
    ? JSON.parse(JSON.stringify(task))
    : {
        id: "",
        name: "每日结构快照",
        targets: [],
        schedule: "daily",
        intervalHours: 24,
        time: "02:00",
        weekdays: [1],
        retentionDays: 90,
        enabled: true,
        background: overview.value.background,
      };
  pickConnection.value = null;
  pickDatabases.value = [];
}
function addTargets() {
  for (const database of pickDatabases.value) {
    const t = { connectionId: pickConnection.value, database };
    if (!edit.value.targets.some((x: any) => key(x) === key(t)))
      edit.value.targets.push(t);
  }
  pickDatabases.value = [];
}
function saveTask() {
  void perform(async () => {
    if (captureOnly.value) {
      await api("Capture", edit.value.targets);
      message.success("采集结束，请查看逐库结果");
    } else {
      await api("SaveTask", edit.value);
      message.success("已保存任务");
    }
    edit.value = null;
    await refresh();
    tab.value = "tasks";
  });
}
async function confirmDelete(title: string, action: () => Promise<any>) {
  if (await confirmBox(title)) {
    await perform(async () => {
      await action();
      await refresh();
    });
  }
}
function compare(live = false) {
  void perform(async () => {
    let beforeId = "",
      afterId = "";
    if (live) {
      beforeId = document.value?.record.id;
      if (!beforeId || !liveConnection.value || !liveDatabase.value)
        throw new Error("请选择快照和当前目标数据库");
    } else {
      if (selected.value.length !== 2) throw new Error("请选择两份快照");
      const chosen = overview.value.snapshots
        .filter((s: any) => selected.value.includes(s.id))
        .sort((a: any, b: any) => a.capturedAt.localeCompare(b.capturedAt));
      beforeId = chosen[0].id;
      afterId = chosen[1].id;
    }
    diff.value = await api("Compare", {
      beforeId,
      afterId,
      live: live
        ? { connectionId: liveConnection.value, database: liveDatabase.value }
        : undefined,
      normalize: options.value,
      restore: false,
    });
    diffTable.value = diff.value.differences[0]?.table || "";
  });
}
function restore() {
  void perform(async () => {
    if (!liveConnection.value || !liveDatabase.value)
      throw new Error("请选择实际目标库");
    if (
      !(await confirmBox(
        `将以此快照为期望结构，重新读取 ${liveDatabase.value} 并进入同步工作台；此步骤不执行 SQL。`,
      ))
    )
      return;
    const target = {
      connectionId: liveConnection.value,
      database: liveDatabase.value,
    };
    const compare = await api(
      "RestoreCompare",
      document.value.record.id,
      target,
      options.value,
    );
    emit("restore", {
      document: document.value,
      target,
      compare,
      options: options.value,
    });
    emit("update:show", false);
  });
}
async function saveFile(filename: string, content: string) {
  const url = URL.createObjectURL(
    new Blob([content], { type: "text/plain;charset=utf-8" }),
  );
  const a = window.document.createElement("a");
  a.href = url;
  a.download = filename;
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
function exportSelected(kind: string) {
  void perform(async () => {
    const ids = selected.value.length
      ? selected.value
      : document.value
        ? [document.value.record.id]
        : [];
    try {
      const path = await api("SaveExport", ids, kind);
      if (path) message.success(`已保存：${path}`);
    } catch (e) {
      if (!String(e).includes("不支持保存对话框")) throw e;
      const f = await api("Export", ids, kind);
      await saveFile(f.filename, f.content);
    }
  });
}
function importFile() {
  void perform(async () => {
    try {
      const raw = await api("ReadImportFile");
      if (raw) {
        const n = await api("Import", raw);
        message.success(`已导入 ${n} 份快照`);
        await refresh();
      }
    } catch (e) {
      if (!String(e).includes("不支持打开对话框")) throw e;
      fileInput.value?.click();
    }
  });
}
async function uploaded(event: Event) {
  const input = event.target as HTMLInputElement;
  const f = input.files?.[0];
  if (!f) return;
  await perform(async () => {
    if (f.size > 64 * 1024 * 1024) throw new Error("快照包不能超过 64 MB");
    const n = await api("Import", await f.text());
    message.success(`已导入 ${n} 份快照`);
    await refresh();
  });
  input.value = "";
}
function exportReport() {
  void perform(async () => {
    try {
      const path = await api("SaveDiffReport", diff.value);
      if (path) message.success(`已保存：${path}`);
    } catch (e) {
      if (!String(e).includes("不支持保存对话框")) throw e;
      await saveFile("结构差异报告.json", JSON.stringify(diff.value, null, 2));
    }
  });
}
function toggleBackground() {
  void perform(async () => {
    await api("SetBackground", !overview.value.background);
    await refresh();
    message.success(
      overview.value.background
        ? "已启用后台调度"
        : "已关闭后台调度，应用运行时仍可执行任务",
    );
  });
}
</script>

<template>
  <n-modal
    :show="show"
    :mask-closable="!busy"
    @update:show="emit('update:show', $event)"
  >
    <section class="snapshot-panel" aria-label="结构快照">
      <header>
        <div>
          <h2>结构快照</h2>
          <p>保存数据库的结构版本，追踪每一次变化</p>
        </div>
        <div class="actions">
          <n-spin v-if="busy" :size="18" /><button
            :disabled="busy"
            @click="perform(refresh)"
          >
            刷新</button
          ><button :disabled="busy" @click="emit('update:show', false)">
            关闭
          </button>
        </div>
      </header>
      <nav>
        <button :class="{ active: tab === 'library' }" @click="tab = 'library'">
          快照库 <small>{{ overview.snapshots.length }}</small></button
        ><button :class="{ active: tab === 'tasks' }" @click="tab = 'tasks'">
          定时任务 <small>{{ overview.tasks.length }}</small></button
        ><span
          >按本机时区调度 ·
          {{
            overview.background ? "后台调度已开启" : "退出应用后暂停采集"
          }}</span
        >
      </nav>
      <div class="toolbar">
        <template v-if="tab === 'library'"
          ><button
            class="primary"
            :disabled="busy || disabled"
            @click="newEdit(undefined, true)"
          >
            立即采集</button
          ><button :disabled="busy" @click="importFile">导入快照包</button
          ><button
            :disabled="busy || (!selected.length && !document)"
            @click="exportSelected('json')"
          >
            导出快照包</button
          ><button
            :disabled="busy || (!selected.length && !document)"
            @click="exportSelected('sql')"
          >
            导出结构 SQL</button
          ><button :disabled="busy || selected.length !== 2" @click="compare()">
            对比所选 {{ selected.length }} / 2</button
          ><button v-if="selected.length" @click="selected = []">
            清空选择
          </button></template
        ><template v-else
          ><button
            class="primary"
            :disabled="busy || disabled"
            @click="newEdit()"
          >
            新增任务</button
          ><button :disabled="busy" @click="toggleBackground">
            {{ overview.background ? "关闭" : "开启" }} macOS 后台调度
          </button></template
        >
      </div>
      <input
        ref="fileInput"
        type="file"
        accept=".json"
        hidden
        @change="uploaded"
      />
      <div v-if="tab === 'library'" class="library">
        <aside class="groups">
          <button :class="{ active: !group }" @click="group = ''">
            全部数据库 <small>{{ overview.snapshots.length }}</small></button
          ><button
            v-for="g in groups"
            :key="g.key"
            :class="{ active: group === g.key }"
            @click="group = g.key"
          >
            <strong>{{ g.database }}</strong
            ><span>{{ g.label }} · {{ g.count }} 份</span>
          </button>
          <p v-if="!groups.length" class="empty">
            还没有结构快照<br />可立即采集或导入快照包
          </p>
        </aside>
        <section class="versions">
          <n-input
            v-model:value="filter"
            placeholder="搜索数据库、连接或备注"
            clearable
          />
          <p class="hint">选择两份快照：旧版本 → 新版本</p>
          <article
            v-for="s in records"
            :key="s.id"
            :class="{ current: document?.record.id === s.id }"
            @click="openRecord(s)"
          >
            <n-checkbox
              :checked="selected.includes(s.id)"
              :disabled="
                busy || (!selected.includes(s.id) && selected.length >= 2)
              "
              @click.stop
              @update:checked="choose(s.id, $event)"
            />
            <div>
              <strong>{{ s.database }}</strong
              ><time>最近备份 {{ time(s.lastCheckedAt || s.capturedAt) }}</time
              ><span
                >{{ s.tableCount }} 张表 ·
                {{
                  s.pinned
                    ? "重要版本"
                    : s.unchanged
                      ? "结构未变化"
                      : "结构版本"
                }}</span
              >
              <p v-if="s.note">{{ s.note }}</p>
            </div>
          </article>
          <p v-if="!records.length" class="empty">暂无快照</p>
        </section>
        <section class="snapshot-detail">
          <template v-if="diff"
            ><div class="detail-title">
              <h3>结构差异 · {{ diff.differences.length }} 张表</h3>
              <button @click="diff = null">返回快照</button
              ><button @click="exportReport">导出报告</button>
            </div>
            <p class="direction">
              {{ diff.before }}<br />↓<br />{{ diff.after }}
            </p>
            <div v-for="(w, i) in diff.warnings" :key="i" class="hint">
              {{ w }}
            </div>
            <p v-if="!diff.differences.length" class="empty">
              在当前对比选项范围内，结构一致
            </p>
            <div v-else class="diff-layout">
              <aside>
                <button
                  v-for="d in diff.differences"
                  :key="d.table"
                  :class="{ active: diffTable === d.table }"
                  @click="diffTable = d.table"
                >
                  {{ d.kind === "add" ? "＋" : d.kind === "drop" ? "－" : "~" }}
                  {{ d.table || "数据库属性" }}
                </button>
              </aside>
              <div v-if="activeDifference">
                <ul>
                  <li v-for="i in activeDifference.items" :key="i.id">
                    {{ i.summary }}
                  </li>
                </ul>
                <div class="ddl-pair">
                  <div>
                    <h4>旧版本</h4>
                    <SQLCode :sql="activeDifference.before || '-- 不存在'" />
                  </div>
                  <div>
                    <h4>新版本</h4>
                    <SQLCode :sql="activeDifference.after || '-- 不存在'" />
                  </div>
                </div>
              </div>
            </div>
            <details v-if="diff.SQL">
              <summary>差异 SQL（仅供审阅，不直接执行）</summary>
              <SQLCode :sql="diff.SQL" /></details
          ></template>
          <template v-else-if="document"
            ><div class="detail-title">
              <div>
                <h3>{{ document.record.database }}</h3>
                <p>
                  {{ document.record.connectionName }} ·
                  最近备份 {{ time(document.record.lastCheckedAt || document.record.capturedAt) }}
                </p>
                <p>版本生成 {{ time(document.record.capturedAt) }}</p>
              </div>
              <button
                :disabled="busy"
                class="danger"
                @click="
                  confirmDelete(
                    '删除这份快照？此操作不会修改数据库。',
                    async () => {
                      await api('Delete', document.record.id);
                      document = null;
                    },
                  )
                "
              >
                删除快照
              </button>
            </div>
            <div class="metrics">
              <span>{{ document.record.tableCount }} 张表</span
              ><span>MySQL {{ document.archive.server.version }}</span
              ><span>可见基础表已采集</span>
            </div>
            <p
              v-for="(w, i) in document.archive.warnings"
              :key="i"
              class="hint"
            >
              {{ w }}
            </p>
            <div class="annotation">
              <n-input v-model:value="note" placeholder="版本备注" /><n-checkbox
                v-model:checked="pinned"
                >重要版本，不自动清理</n-checkbox
              ><button
                :disabled="busy"
                @click="
                  perform(async () => {
                    await api('Annotate', document.record.id, note, pinned);
                    await refresh();
                    message.success('已保存备注');
                  })
                "
              >
                保存
              </button>
            </div>
            <section class="compare-live">
              <h4>与当前数据库对比</h4>
              <div class="actions">
                <n-select
                  v-model:value="liveConnection"
                  :options="connectionOptions"
                  placeholder="选择连接"
                /><n-select
                  v-model:value="liveDatabase"
                  :options="
                    liveDatabases.map((value) => ({ label: value, value }))
                  "
                  placeholder="选择数据库"
                  filterable
                />
              </div>
              <div class="actions">
                <button
                  :disabled="busy || disabled || !liveDatabase"
                  @click="compare(true)"
                >
                  查看当前变化</button
                ><button
                  :disabled="busy || disabled || !liveDatabase"
                  @click="restore"
                >
                  以此快照为期望结构…
                </button>
              </div>
            </section>
            <details v-for="t in document.archive.tables" :key="t.table">
              <summary>{{ t.table }}</summary>
              <SQLCode :sql="t.ddl" />
            </details>
            <p class="hash">SHA256 {{ document.record.hash }}</p></template
          >
          <div v-else class="empty large">
            选择一份快照查看结构<br />或勾选两个版本对比变化
          </div>
        </section>
      </div>
      <div v-else class="task-content">
        <p class="hint">
          结构未变化时复用已有快照，只记录执行结果；仅结构变化时保存新版本。睡眠或关机错过执行时，恢复后补采一次。后台模式在登录状态下运行，无需打开窗口。失败不会覆盖已有快照。
        </p>
        <table>
          <thead>
            <tr>
              <th>任务 / 数据库</th>
              <th>周期与运行方式</th>
              <th>下次执行</th>
              <th>最近执行 / 最近成功</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="t in overview.tasks" :key="t.id">
              <td>
                <b>{{ t.name }}</b>
                <details>
                  <summary>{{ t.targets.length }} 个数据库</summary>
                  <p v-for="x in t.targets" :key="key(x)">
                    {{ targetLabel(x) }}
                  </p>
                </details>
              </td>
              <td>
                {{ taskSchedule(t) }}
                <p>
                  {{ t.background ? "后台执行" : "应用运行时" }} · 保留
                  {{ t.retentionDays }} 天
                </p>
              </td>
              <td>
                {{ t.enabled ? time(t.nextRun) : "已暂停" }}
                <p
                  v-if="t.enabled && t.background && !overview.background"
                  class="danger"
                >
                  后台未开启，仅应用运行时执行
                </p>
              </td>
              <td>
                {{
                  status(
                    overview.runs.find((r: any) => r.taskId === t.id)?.status ||
                      "尚未执行",
                  )
                }}
                <p>
                  {{
                    time(
                      overview.runs.find((r: any) => r.taskId === t.id)
                        ?.startedAt,
                    )
                  }}
                </p>
                <p class="hint">
                  最近成功：{{
                    time(
                      overview.runs.find(
                        (r: any) => r.taskId === t.id && r.status === "success",
                      )?.finishedAt,
                    )
                  }}
                </p>
              </td>
              <td>
                <div class="actions">
                  <button
                    :disabled="busy || disabled"
                    @click="
                      perform(async () => {
                        await api('RunTask', t.id);
                        await refresh();
                        message.success('执行结束，请查看逐库结果');
                      })
                    "
                  >
                    立即执行</button
                  ><button :disabled="busy || disabled" @click="newEdit(t)">
                    编辑</button
                  ><button
                    :disabled="busy || disabled"
                    @click="
                      perform(async () => {
                        await api('SaveTask', { ...t, enabled: !t.enabled });
                        await refresh();
                      })
                    "
                  >
                    {{ t.enabled ? "暂停" : "启用" }}</button
                  ><button
                    :disabled="busy"
                    class="danger"
                    @click="
                      confirmDelete('删除任务？已有快照会保留。', () =>
                        api('DeleteTask', t.id),
                      )
                    "
                  >
                    删除
                  </button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
        <p v-if="!overview.tasks.length" class="empty">
          暂无定时任务，点击“新增任务”开始配置
        </p>
        <h3>执行记录</h3>
        <details v-for="r in overview.runs" :key="r.id">
          <summary>
            {{ time(r.startedAt) }} · {{ r.taskName }} · {{ status(r.status) }}
            {{ r.catchUp ? "（补采）" : "" }} · 成功
            {{ r.results.filter((x: any) => x.snapshotId).length }} /
            {{ r.results.length }}
          </summary>
          <p>完成时间：{{ time(r.finishedAt) }}</p>
          <p v-if="r.status === 'running'" class="hint">
            若应用或电脑意外退出，部分数据库可能未采集；使用任务“立即执行”重新检查。
          </p>
          <p v-for="(x, i) in r.results" :key="i" :class="{ danger: x.error }">
            {{ x.connectionName }} / {{ x.target.database }} ·
            {{ x.error || (x.unchanged ? "结构未变化，复用已有快照" : "成功") }} · {{ x.durationMs }} ms
          </p>
          <button
            v-if="r.results.some((x: any) => x.error)"
            :disabled="busy || disabled"
            @click="
              perform(async () => {
                await api('RetryRun', r.id);
                await refresh();
              })
            "
          >
            重试失败项
          </button>
        </details>
      </div>
      <footer v-if="tab === 'library'">
        <n-checkbox
          :checked="!options.ignoreIndexes"
          @update:checked="
            options.ignoreIndexes = !$event;
            diff = null;
          "
          >对比索引</n-checkbox
        ><n-checkbox
          v-model:checked="options.ignoreAutoIncrement"
          @update:checked="diff = null"
          >忽略 AUTO_INCREMENT</n-checkbox
        ><n-checkbox
          v-model:checked="options.stripIntDisplayWidth"
          @update:checked="diff = null"
          >忽略整数显示宽度</n-checkbox
        ><n-checkbox
          v-model:checked="options.unifyUtf8mb4CiCollations"
          @update:checked="diff = null"
          >统一常用排序规则</n-checkbox
        ><span>快照不含业务数据，不能恢复被删除的数据</span>
      </footer>
    </section>
  </n-modal>
  <n-modal
    :show="!!edit"
    preset="card"
    :title="
      captureOnly
        ? '立即采集数据库结构'
        : edit?.id
          ? '编辑定时任务'
          : '新增定时任务'
    "
    style="width: 680px; max-width: 95vw"
    :mask-closable="!busy"
    @update:show="edit = $event ? edit : null"
    ><div v-if="edit" class="snapshot-editor">
      <template v-if="!captureOnly"
        ><label>任务名称<n-input v-model:value="edit.name" /></label></template
      ><label
        >选择连接<n-select
          v-model:value="pickConnection"
          :options="connectionOptions" /></label
      ><label
        >选择数据库（可多选）<n-select
          v-model:value="pickDatabases"
          multiple
          filterable
          :loading="dbLoading"
          :options="databases.map((value) => ({ label: value, value }))"
          :disabled="!pickConnection" /></label
      ><button :disabled="!pickDatabases.length" @click="addTargets">
        添加到采集列表
      </button>
      <div class="target-list">
        <p v-for="(t, i) in edit.targets" :key="key(t)">
          {{ targetLabel(t) }}
          <button @click="edit.targets.splice(i, 1)">移除</button>
        </p>
        <p v-if="!edit.targets.length">
          从连接中选择一个或多个数据库，并点击添加
        </p>
      </div>
      <template v-if="!captureOnly"
        ><div class="form-row">
          <label
            >执行周期<n-select
              v-model:value="edit.schedule"
              :options="[
                { label: '每天', value: 'daily' },
                { label: '每周', value: 'weekly' },
                { label: '每隔若干小时', value: 'interval' },
              ]" /></label
          ><label v-if="edit.schedule === 'interval'"
            >间隔小时<n-input-number
              v-model:value="edit.intervalHours"
              :min="1"
              :max="8760" /></label
          ><label v-else
            >执行时间（本机时区）<input v-model="edit.time" type="time"
          /></label>
        </div>
        <n-select
          v-if="edit.schedule === 'weekly'"
          v-model:value="edit.weekdays"
          multiple
          :options="
            [1, 2, 3, 4, 5, 6, 0].map((value) => ({
              label: '星期' + '日一二三四五六'[value],
              value,
            }))
          "
          placeholder="选择星期"
        /><label
          >保留天数（重要版本不清理）<n-input-number
            v-model:value="edit.retentionDays"
            :min="1"
            :max="36500" /></label
        ><n-checkbox v-model:checked="edit.enabled">启用任务</n-checkbox
        ><n-checkbox v-model:checked="edit.background"
          >后台执行（需先开启 macOS 后台调度）</n-checkbox
        ></template
      >
      <div class="actions">
        <button :disabled="busy" @click="edit = null">取消</button
        ><button
          class="primary"
          :disabled="busy || disabled || !edit.targets.length"
          @click="saveTask"
        >
          {{ busy ? "处理中…" : captureOnly ? "开始采集" : "保存任务" }}
        </button>
      </div>
    </div></n-modal
  >
</template>

<style scoped>
.snapshot-panel {
  position: fixed;
  inset: 22px;
  height: calc(100dvh - 44px);
  margin: 0;
  box-sizing: border-box;
  min-height: 0;
  display: flex;
  flex-direction: column;
  background: #f7f9fa;
  color: #243237;
  border: 1px solid #dce3e5;
  border-radius: 12px;
  overflow: hidden;
  box-shadow: 0 18px 90px #10202630;
  font-size: 13px;
}
.snapshot-panel header,
.snapshot-panel nav,
.snapshot-panel .toolbar,
.snapshot-panel footer {
  flex-shrink: 0;
}
.snapshot-panel header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 18px 24px;
  background: white;
  border-bottom: 1px solid #e0e7e8;
}
h2,
h3,
h4,
p {
  margin: 0;
}
h2 {
  font-size: 21px;
}
header p,
.hint,
.hash {
  color: #78878c;
  font-size: 12px;
  line-height: 1.7;
}
header p {
  margin-top: 3px;
}
button {
  font: inherit;
  cursor: pointer;
  border: 1px solid #d7e1e2;
  background: #fff;
  color: #34484c;
  border-radius: 5px;
  padding: 6px 11px;
  transition: background 0.15s;
}
button:hover:not(:disabled) {
  background: #eaf5f1;
}
button:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}
button.primary {
  background: #147565;
  color: white;
  border-color: #147565;
}
.actions {
  display: flex;
  gap: 8px;
  align-items: center;
  flex-wrap: wrap;
}
.actions :deep(.n-select) {
  width: 220px;
}
nav {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 0 24px;
  background: white;
  border-bottom: 1px solid #dce3e5;
}
nav button {
  border: 0;
  border-radius: 0;
  padding: 13px 16px;
  border-bottom: 2px solid transparent;
}
nav button.active {
  border-color: #147565;
  color: #147565;
}
nav span {
  margin-left: auto;
  color: #7a898e;
  font-size: 12px;
}
small {
  color: #829397;
}
nav small {
  margin-left: 7px;
}
.toolbar {
  padding: 12px 24px;
  display: flex;
  gap: 8px;
  border-bottom: 1px solid #dce3e5;
  background: white;
  flex-wrap: wrap;
}
.library {
  display: grid;
  grid-template-columns: 200px 280px minmax(0, 1fr);
  min-height: 0;
  flex: 1;
}
.groups,
.versions,
.snapshot-detail,
.task-content {
  overflow: auto;
  min-height: 0;
}
.groups {
  background: #eef3f4;
  border-right: 1px solid #dce3e5;
  padding: 12px 8px;
}
.groups button {
  width: 100%;
  text-align: left;
  border: 0;
  background: transparent;
  padding: 10px;
  word-break: break-word;
}
.groups strong,
.groups span {
  display: block;
}
.groups span {
  font-size: 11px;
  color: #76878d;
  margin-top: 5px;
}
.groups button.active,
button.active {
  background: #dcefe9;
  color: #126c5d;
}
.versions {
  padding: 12px;
  border-right: 1px solid #dce3e5;
}
.versions .hint {
  margin: 9px 0;
}
.versions article {
  padding: 13px 10px;
  display: flex;
  gap: 10px;
  border: 1px solid #dfe6e8;
  border-radius: 6px;
  background: #fff;
  margin: 8px 0;
  cursor: pointer;
  word-break: break-word;
}
.versions article.current {
  border-color: #178675;
  background: #f0faf6;
}
.versions time,
.versions span {
  display: block;
  font-size: 11px;
  margin-top: 5px;
  color: #7d8c91;
}
.versions p {
  margin-top: 5px;
}
.snapshot-detail {
  padding: 20px;
  background: #fff;
}
.detail-title {
  display: flex;
  gap: 10px;
  align-items: center;
  justify-content: space-between;
}
.detail-title h3 {
  font-size: 18px;
}
.detail-title p {
  font-size: 12px;
  color: #7a8b90;
  margin-top: 5px;
}
.metrics {
  display: flex;
  gap: 8px;
  margin: 15px 0;
}
.metrics span {
  padding: 5px 9px;
  background: #edf5f2;
  border-radius: 4px;
}
.annotation {
  display: flex;
  gap: 9px;
  align-items: center;
  flex-wrap: wrap;
  margin: 18px 0;
}
.annotation :deep(.n-input) {
  max-width: 360px;
}
.compare-live {
  padding: 14px;
  background: #f2f7f8;
  border: 1px solid #dce5e7;
  border-radius: 6px;
  margin-bottom: 18px;
}
.compare-live h4 {
  margin-bottom: 10px;
}
.compare-live .actions {
  margin-top: 9px;
}
details {
  padding: 10px 0;
  border-bottom: 1px solid #e2e8ea;
}
summary {
  cursor: pointer;
  overflow-wrap: anywhere;
}
details :deep(.sql-code) {
  margin-top: 10px;
}
footer {
  border-top: 1px solid #dce3e5;
  padding: 12px 20px;
  display: flex;
  gap: 16px;
  align-items: center;
  flex-wrap: wrap;
  background: #fff;
}
footer span {
  font-size: 11px;
  color: #7b898d;
  margin-left: auto;
}
.empty {
  padding: 32px 10px;
  color: #8b989d;
  line-height: 2;
  text-align: center;
}
.large {
  padding-top: 140px;
}
.hash {
  word-break: break-all;
  margin-top: 20px;
}
.task-content {
  padding: 18px 24px;
  flex: 1;
}
.task-content table {
  width: 100%;
  border-collapse: collapse;
  background: white;
  margin: 12px 0 26px;
}
.task-content th {
  text-align: left;
  color: #74858a;
  background: #eff4f5;
}
.task-content td,
.task-content th {
  padding: 13px;
  border-bottom: 1px solid #e0e7e9;
  vertical-align: top;
}
.task-content td p {
  font-size: 12px;
  margin-top: 6px;
}
.task-content h3 {
  margin: 18px 0 8px;
}
.task-content details p {
  margin: 8px 0;
}
.danger {
  color: #bf423c;
}
.direction {
  padding: 12px;
  background: #eef6f3;
  margin: 12px 0;
  line-height: 1.8;
  overflow-wrap: anywhere;
}
.diff-layout {
  display: grid;
  grid-template-columns: 160px minmax(0, 1fr);
  gap: 14px;
  margin: 15px 0;
}
.diff-layout aside button {
  display: block;
  width: 100%;
  text-align: left;
  overflow-wrap: anywhere;
  margin-bottom: 5px;
}
.ddl-pair {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  gap: 12px;
}
.ddl-pair h4 {
  margin: 12px 0;
}
.snapshot-editor {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.snapshot-editor label {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.snapshot-editor button {
  align-self: flex-start;
}
.snapshot-editor input[type="time"] {
  font: inherit;
  border: 1px solid #dce3e5;
  border-radius: 4px;
  padding: 7px;
}
.form-row {
  display: flex;
  gap: 18px;
}
.form-row label {
  flex: 1;
}
.target-list {
  max-height: 180px;
  overflow: auto;
  padding: 10px;
  background: #f0f5f4;
  border-radius: 6px;
}
.target-list p {
  margin: 5px 0;
  overflow-wrap: anywhere;
}
.target-list button {
  padding: 2px 6px;
  margin-left: 8px;
}
@media (min-width: 1600px) {
  .library {
    grid-template-columns: 240px 320px minmax(0, 1fr);
  }
}
@media (max-width: 1100px) {
  .snapshot-panel {
    inset: 10px;
    height: calc(100dvh - 20px);
  }
  .library {
    grid-template-columns: 160px 240px minmax(0, 1fr);
  }
  .ddl-pair {
    grid-template-columns: minmax(0, 1fr);
  }
  nav span {
    max-width: 300px;
  }
  .diff-layout {
    grid-template-columns: 120px minmax(0, 1fr);
  }
}
</style>
