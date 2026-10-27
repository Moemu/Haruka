package moe.snowy.haruka.sync

import kotlinx.coroutines.flow.MutableStateFlow

/** 仪表盘服务状态卡的数据源；由 HeartbeatService 更新，进程内共享。 */
data class RuntimeState(
    val running: Boolean = false,
    val phase: String = PHASE_STOPPED, // stopped | starting | ok | waiting | auth | paused
    /** 服务本次运行的起点（心跳循环启动时刻），供仪表盘 FAB 显示运行时长；停止后归零。 */
    val startedAtMs: Long = 0,
    val lastSyncMs: Long = 0,
    val nextTickMs: Long = 0,
    val queue: Int = 0,
) {
    companion object {
        const val PHASE_STOPPED = "stopped"
        const val PHASE_STARTING = "starting"
        const val PHASE_OK = "ok"
        const val PHASE_WAITING = "waiting"
        const val PHASE_AUTH = "auth"
        const val PHASE_PAUSED = "paused"
    }
}

object ServiceRuntime {
    val state = MutableStateFlow(RuntimeState())
}

/**
 * 观测回路阈值（spec §5.4）：距上次成功同步超过 3 × 登记间隔即视为疑似被系统限制，
 * 仪表盘提示卡据此显示。Doze 期间小时级新鲜度属预期，靠间隔本身吸收，不算额外判断。
 */
fun isSyncStale(nowMs: Long, lastSyncMs: Long, heartbeatMin: Int): Boolean {
    if (lastSyncMs <= 0 || heartbeatMin <= 0) return false
    return nowMs - lastSyncMs > heartbeatMin * 3 * 60_000L
}
