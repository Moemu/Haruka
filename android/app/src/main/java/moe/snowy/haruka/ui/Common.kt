package moe.snowy.haruka.ui

import java.time.Instant
import java.time.ZoneId
import java.time.format.DateTimeFormatter

/** 相对时间显示：与状态页「x 分钟前」口径一致。 */
fun relativeTime(epochMs: Long, nowMs: Long = System.currentTimeMillis()): String = when {
    epochMs <= 0 -> "—"
    nowMs - epochMs < 60_000 -> "刚刚"
    nowMs - epochMs < 3_600_000 -> "${(nowMs - epochMs) / 60_000} 分钟前"
    else -> "${(nowMs - epochMs) / 3_600_000} 小时前"
}

fun shortTime(epochMs: Long): String =
    DateTimeFormatter.ofPattern("HH:mm").withZone(ZoneId.systemDefault())
        .format(Instant.ofEpochMilli(epochMs))

fun shortDateTime(epochMs: Long): String =
    DateTimeFormatter.ofPattern("MM-dd HH:mm").withZone(ZoneId.systemDefault())
        .format(Instant.ofEpochMilli(epochMs))
