package moe.snowy.haruka.protocol

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.encodeToJsonElement
import kotlinx.serialization.json.put
import java.time.Instant
import java.time.ZoneOffset
import java.time.format.DateTimeFormatter
import java.util.UUID

/**
 * 三端共享的上报协议（契约源：protocol/events.go 与 schema/envelope.schema.json）。
 * 改字段必须同步 Go 端与金样 fixture，金样测试守护漂移。
 */
const val PROTOCOL_VERSION = 1
const val DEVICE_KIND_ANDROID = "android"

const val TYPE_HEARTBEAT = "heartbeat"
const val TYPE_PRESENCE = "presence"
const val TYPE_ACTIVITY = "activity"
const val TYPE_LOCATION = "location"
const val TYPE_MANUAL = "manual"
const val TYPE_DEVICE = "device"

const val STATE_ACTIVE = "active"
const val STATE_AWAY = "away"

const val PRECISION_COUNTRY = "country"
const val PRECISION_PROVINCE = "province"
const val PRECISION_CITY = "city"
const val PRECISION_DISTRICT = "district"

const val ZONE_HOME = "home"

const val SOURCE_IP = "ip"
const val SOURCE_GPS_COARSE = "gps_coarse"

@Serializable
data class Envelope(
    // v 与 deviceKind 无默认值：Go 端总是序列化这两个字段，encodeDefaults=false 不得省略
    @SerialName("v") val v: Int,
    @SerialName("device_id") val deviceId: String,
    @SerialName("device_kind") val deviceKind: String,
    @SerialName("sent_at") val sentAt: String,
    @SerialName("snapshot") val snapshot: Boolean = false,
    @SerialName("events") val events: List<Event>,
)

@Serializable
data class Event(
    @SerialName("id") val id: String,
    @SerialName("seq") val seq: Long,
    @SerialName("ts") val ts: String,
    @SerialName("type") val type: String,
    @SerialName("data") val data: JsonElement,
)

@Serializable
data class PresenceData(
    @SerialName("state") val state: String,
    @SerialName("idle_minutes") val idleMinutes: Int,
)

@Serializable
data class LocationData(
    @SerialName("source") val source: String,
    @SerialName("precision") val precision: String,
    @SerialName("country") val country: String? = null,
    @SerialName("province") val province: String? = null,
    @SerialName("city") val city: String? = null,
    @SerialName("district") val district: String? = null,
    @SerialName("zone") val zone: String? = null,
)

@Serializable
data class ManualData(
    @SerialName("mood") val mood: String? = null,
    @SerialName("text") val text: String? = null,
    @SerialName("until") val until: String? = null,
    @SerialName("clear") val clear: Boolean = false,
)

/** activity 负载（契约源 protocol/events.go）：kind 必填，app/category 省略即空。 */
@Serializable
data class ActivityData(
    @SerialName("kind") val kind: String,
    @SerialName("app") val app: String? = null,
    @SerialName("category") val category: String? = null,
)

/** 设备自述状态：电量必填；charging/steps 只在非默认时携带（对齐 Go omitempty）。 */
@Serializable
data class DeviceData(
    @SerialName("battery_level") val batteryLevel: Int,
    @SerialName("charging") val charging: Boolean = false,
    @SerialName("steps") val steps: Int = 0,
)

@Serializable
object HeartbeatData

/** 协议 mood 枚举，与服务端 validMoods 一致（server/ingest.go）。 */
val VALID_MOODS = listOf("happy", "focused", "tired", "anxious", "chilling", "sick")

object Wire {
    /** 严格模式：未知字段不忽略，与服务端 decodeStrict 同向；null 值不编码（对齐 Go omitempty）。 */
    val json = Json {
        explicitNulls = false
        encodeDefaults = false
    }

    private val rfc3339 =
        DateTimeFormatter.ofPattern("yyyy-MM-dd'T'HH:mm:ss'Z'").withZone(ZoneOffset.UTC)

    /** Go time.Parse(RFC3339) 要求秒位，Instant.toString() 会省略整分秒，必须走 formatter。 */
    fun rfc3339(epochMs: Long): String = rfc3339.format(Instant.ofEpochMilli(epochMs))

    fun newEventId(): String = "e_" + UUID.randomUUID().toString().replace("-", "")

    fun envelope(deviceId: String, snapshot: Boolean, events: List<Event>): String =
        json.encodeToString(
            Envelope.serializer(),
            Envelope(
                v = PROTOCOL_VERSION,
                deviceId = deviceId,
                deviceKind = DEVICE_KIND_ANDROID,
                sentAt = rfc3339(System.currentTimeMillis()),
                snapshot = snapshot,
                events = events,
            ),
        )

    private fun event(type: String, seq: Long, nowMs: Long, data: JsonElement) =
        Event(id = newEventId(), seq = seq, ts = rfc3339(nowMs), type = type, data = data)

    fun heartbeatEvent(seq: Long, nowMs: Long) = event(TYPE_HEARTBEAT, seq, nowMs, HeartbeatData.json())

    fun presenceEvent(seq: Long, nowMs: Long, data: PresenceData) =
        event(TYPE_PRESENCE, seq, nowMs, json.encodeToJsonElement(PresenceData.serializer(), data))

    fun locationEvent(seq: Long, nowMs: Long, data: LocationData) =
        event(TYPE_LOCATION, seq, nowMs, json.encodeToJsonElement(LocationData.serializer(), data))

    fun activityEvent(seq: Long, nowMs: Long, data: ActivityData) =
        event(TYPE_ACTIVITY, seq, nowMs, json.encodeToJsonElement(ActivityData.serializer(), data))

    fun deviceEvent(seq: Long, nowMs: Long, data: DeviceData) =
        event(TYPE_DEVICE, seq, nowMs, json.encodeToJsonElement(DeviceData.serializer(), data))

    fun manualEvent(seq: Long, nowMs: Long, data: ManualData) =
        event(TYPE_MANUAL, seq, nowMs, json.encodeToJsonElement(ManualData.serializer(), data))

    private fun HeartbeatData.json(): JsonObject = buildJsonObject { }
}

/**
 * presence 亮屏启发式（spec §6.1 定稿）：亮屏、未锁屏、且距最近亮屏/解锁不超过
 * activeWindowMs 记 active，否则 away，空闲分钟数从最近活跃时刻起算。无任何权限。
 */
fun presenceSample(
    interactive: Boolean,
    keyguardLocked: Boolean,
    nowMs: Long,
    lastActiveMs: Long,
    activeWindowMs: Long = 10 * 60 * 1000L,
    maxIdleMinutes: Int = 24 * 60,
): PresenceData {
    val withinWindow = lastActiveMs > 0 && (nowMs - lastActiveMs) <= activeWindowMs
    return if (interactive && !keyguardLocked && withinWindow) {
        PresenceData(state = STATE_ACTIVE, idleMinutes = 0)
    } else {
        val idleMin = if (lastActiveMs > 0) ((nowMs - lastActiveMs) / 60000L).toInt() else maxIdleMinutes
        PresenceData(state = STATE_AWAY, idleMinutes = idleMin.coerceAtMost(maxIdleMinutes))
    }
}

/**
 * 手动附言的服务端预检（server/ingest.go checkDisplayString 的客户端镜像）：
 * 码点 ≤60、禁止 URL/坐标对/路径分隔符/IPv4，命中即拒绝发布。
 */
fun validateManualText(text: String): String? {
    if (text.isEmpty()) return null
    if (text.codePointCount(0, text.length) > 60) return "附言超过 60 字"
    if (text.toByteArray(Charsets.UTF_8).size > 240) return "附言超过 60 字"
    if (text.contains("://")) return "附言不能包含链接"
    if (Regex("\\s*-?\\d+\\.\\d+\\s*,").containsMatchIn(text)) return "附言疑似坐标"
    if (text.contains('/') || text.contains('\\')) return "附言不能包含路径"
    if (Regex("\\d{1,3}\\.\\d{1,3}\\.\\d{1,3}\\.\\d{1,3}").containsMatchIn(text)) return "附言疑似 IP 地址"
    return null
}

/** 「今天」有效期：本地时区的明日零点，换算为 UTC 绝对时间。 */
fun endOfTodayUtcMs(nowMs: Long, zoneId: java.time.ZoneId = java.time.ZoneId.systemDefault()): Long {
    val now = Instant.ofEpochMilli(nowMs).atZone(zoneId)
    val tomorrowStart = now.toLocalDate().plusDays(1).atStartOfDay(zoneId)
    return tomorrowStart.toInstant().toEpochMilli()
}
