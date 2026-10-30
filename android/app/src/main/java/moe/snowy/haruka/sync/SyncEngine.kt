package moe.snowy.haruka.sync

import moe.snowy.haruka.data.LogStore
import moe.snowy.haruka.data.Settings
import moe.snowy.haruka.data.SettingsRepository
import moe.snowy.haruka.data.Spool
import moe.snowy.haruka.net.ApiClient
import moe.snowy.haruka.net.HttpResult
import moe.snowy.haruka.protocol.ActivityData
import moe.snowy.haruka.protocol.DeviceData
import moe.snowy.haruka.protocol.Event
import moe.snowy.haruka.protocol.TYPE_HEARTBEAT
import moe.snowy.haruka.protocol.Wire
import moe.snowy.haruka.protocol.presenceSample

/**
 * 单次同步节拍，服务循环与 WorkManager 兜底共用。
 * 批量规则与桌面端一致：单包 ≤100；重连第一包 snapshot=true 保留真实观测时间；
 * heartbeat 不入 spool；失败指数退避 1 分钟起步、30 分钟封顶。
 */
class SyncEngine(
    private val api: ApiClient,
    private val spool: Spool,
    private val settingsRepo: SettingsRepository,
    private val logs: LogStore,
) {
    var connected = false
        private set

    data class TickOutcome(
        val ok: Boolean,
        val authError: Boolean,
        val queueLeft: Int,
        val sentCount: Int,
        val retryDelayMs: Long,
    )

    suspend fun tick(
        settings: Settings,
        interactive: Boolean,
        keyguardLocked: Boolean,
        lastActiveMs: Long,
        activity: ActivityData? = null,
        device: DeviceData? = null,
        nowMs: Long = System.currentTimeMillis(),
    ): TickOutcome {
        // 未配置时绝不发起网络请求（防御 BootReceiver/Worker 的边缘路径）。
        if (!settings.configured) {
            return TickOutcome(ok = false, authError = false, queueLeft = 0, sentCount = 0, retryDelayMs = 0)
        }
        val presence = presenceSample(interactive, keyguardLocked, nowMs, lastActiveMs)

        // 实时观测（heartbeat/presence/activity/device）随包生成，不占 spool。
        val realtime = buildList {
            add(Wire.heartbeatEvent(spool.nextSeq(), nowMs))
            add(Wire.presenceEvent(spool.nextSeq(), nowMs, presence))
            activity?.let { add(Wire.activityEvent(spool.nextSeq(), nowMs, it)) }
            device?.let { add(Wire.deviceEvent(spool.nextSeq(), nowMs, it)) }
        }
        val backlog = spool.peek(BATCH_CAP - realtime.size)
        val events = buildList {
            addAll(realtime)
            backlog.forEach { row ->
                add(Event(id = row.id, seq = row.seq, ts = row.ts, type = row.type, data = Wire.json.parseToJsonElement(row.data)))
            }
        }

        val snapshot = !connected
        val envelope = Wire.envelope(settings.deviceId, snapshot, events)
        return when (val result = api.ingest(settings.baseUrl, settings.deviceToken, envelope)) {
            is HttpResult.Ok -> {
                spool.delete(backlog.map { it.id })
                connected = true
                TickOutcome(ok = true, authError = false, queueLeft = spool.count(), sentCount = events.size, retryDelayMs = 0)
            }
            is HttpResult.Http -> {
                connected = false
                val auth = result.code == 401 || result.code == 403
                logs.log("ingest", "HTTP ${result.code}${if (auth) " 令牌被拒绝" else ""}")
                TickOutcome(ok = false, authError = auth, queueLeft = spool.count(), sentCount = 0, retryDelayMs = if (auth) RETRY_MAX_MS else nextBackoffMs())
            }
            is HttpResult.Network -> {
                connected = false
                logs.log("ingest", "网络不可达：${result.message}")
                TickOutcome(ok = false, authError = false, queueLeft = spool.count(), sentCount = 0, retryDelayMs = nextBackoffMs())
            }
        }
    }

    private var backoffMs = 0L

    private fun nextBackoffMs(): Long {
        backoffMs = if (backoffMs == 0L) RETRY_START_MS else (backoffMs * 2).coerceAtMost(RETRY_MAX_MS)
        return backoffMs
    }

    companion object {
        const val BATCH_CAP = 100
        const val RETRY_START_MS = 60_000L
        const val RETRY_MAX_MS = 30 * 60_000L
    }
}
