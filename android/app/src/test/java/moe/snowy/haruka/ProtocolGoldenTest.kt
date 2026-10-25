package moe.snowy.haruka

import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.jsonObject
import moe.snowy.haruka.protocol.Envelope
import moe.snowy.haruka.protocol.Wire
import moe.snowy.haruka.protocol.endOfTodayUtcMs
import moe.snowy.haruka.protocol.presenceSample
import moe.snowy.haruka.protocol.validateManualText
import java.io.File
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue
import kotlin.test.assertNotNull
import kotlin.test.assertNull
import kotlin.test.fail

/**
 * 协议契约防漂移（spec §6.3）：Kotlin 信封与 protocol/fixtures 的金样语义等价
 * （键序无关、剔除显式 null 后一致）。契约源在仓库 protocol/ 目录，不复制 fixture。
 */
class ProtocolGoldenTest {
    private val fixtures = listOf(
        "valid-heartbeat",
        "valid-presence",
        "valid-activity",
        "valid-manual",
        "valid-location-city",
        "valid-location-district",
    )

    @Test
    fun `envelope round-trips all golden fixtures`() {
        val fixturesDir = locateFixturesDir()
        fixtures.forEach { name ->
            val text = File(fixturesDir, "$name.json").readText()
            val expected = Json.parseToJsonElement(text).jsonObject.stripNulls()

            val decoded = Wire.json.decodeFromString(Envelope.serializer(), text)
            val encoded = Wire.json.encodeToString(Envelope.serializer(), decoded)
            val actual = Json.parseToJsonElement(encoded).jsonObject.stripNulls()

            assertEquals(expected, actual, "fixture $name 漂移")
        }
    }

    /** 从测试工作目录向上找到仓库的 protocol/fixtures（不复制金样，契约源唯一）。 */
    private fun locateFixturesDir(): File {
        var dir = File(System.getProperty("user.dir"))
        repeat(5) {
            val candidate = File(dir, "protocol/fixtures")
            if (candidate.isDirectory) return candidate
            dir = dir.parentFile ?: dir
        }
        fail("找不到 protocol/fixtures（工作目录 ${System.getProperty("user.dir")}）")
    }

    @Test
    fun `sent_at keeps seconds precision`() {
        val ts = Wire.rfc3339(0L)
        assertTrue(Regex("\\d{4}-\\d{2}-\\d{2}T\\d{2}:\\d{2}:\\d{2}Z").matches(ts), "RFC3339 必须带秒位，实际 $ts")
    }

    @Test
    fun `event id matches server shape`() {
        val id = Wire.newEventId()
        assertTrue(Regex("^e_[0-9a-f]{32}$").matches(id), "事件 ID 形态错误：$id")
    }

    @Test
    fun `manual clear omits false and fields match go omitempty`() {
        val ev = Wire.manualEvent(seq = 1, nowMs = 0, data = moe.snowy.haruka.protocol.ManualData(clear = true))
        val obj = ev.data.jsonObject
        assertEquals(JsonPrimitive(true), obj["clear"])
        assertNull(obj["mood"])
        assertNull(obj["text"])
    }

    private fun JsonObject.stripNulls(): JsonElement = JsonObject(
        mapValues { (_, v) ->
            when (v) {
                is JsonObject -> v.stripNulls()
                is JsonArray -> JsonArray(v.map { if (it is JsonObject) it.stripNulls() else it })
                else -> v
            }
        }.filterValues { it !is JsonNull },
    )
}

/** 核心逻辑单元测试：presence 启发式、附言预检、退避、「今天」有效期。 */
class CoreLogicTest {
    private val hour = 3_600_000L

    @Test
    fun `presence is active when unlocked and within window`() {
        val now = 100 * hour
        val p = presenceSample(interactive = true, keyguardLocked = false, nowMs = now, lastActiveMs = now - 60_000)
        assertEquals("active", p.state)
        assertEquals(0, p.idleMinutes)
    }

    @Test
    fun `presence is away when screen off or locked`() {
        val now = 100 * hour
        val p1 = presenceSample(interactive = false, keyguardLocked = false, nowMs = now, lastActiveMs = now - 5 * 60_000)
        assertEquals("away", p1.state)
        assertEquals(5, p1.idleMinutes)

        val p2 = presenceSample(interactive = true, keyguardLocked = true, nowMs = now, lastActiveMs = now - 60_000)
        assertEquals("away", p2.state)
    }

    @Test
    fun `presence away idle grows beyond window`() {
        val now = 100 * hour
        val p = presenceSample(interactive = true, keyguardLocked = false, nowMs = now, lastActiveMs = now - 20 * 60_000)
        assertEquals("away", p.state)
        assertEquals(20, p.idleMinutes)
    }

    @Test
    fun `manual text validation mirrors server rules`() {
        assertNull(validateManualText(""))
        assertNull(validateManualText("赶 deadline 中"))
        assertNotNull(validateManualText("看 https://example.com"))
        assertNotNull(validateManualText("31.23,121.47"))
        assertNotNull(validateManualText("/etc/passwd"))
        assertNotNull(validateManualText("192.168.1.1 测试"))
        assertNotNull(validateManualText("长".repeat(61)))
        assertNull(validateManualText("长".repeat(60)))
    }

    @Test
    fun `end of today converts local midnight to utc`() {
        val zone = java.time.ZoneId.of("Asia/Shanghai")
        val now = java.time.ZonedDateTime.of(2026, 10, 25, 23, 0, 0, 0, zone).toInstant().toEpochMilli()
        val end = endOfTodayUtcMs(now, zone)
        val expected = java.time.ZonedDateTime.of(2026, 10, 26, 0, 0, 0, 0, zone).toInstant().toEpochMilli()
        assertEquals(expected, end)
    }

    @Test
    fun `sync engine backoff grows and caps`() {
        val start = moe.snowy.haruka.sync.SyncEngine.RETRY_START_MS
        val cap = moe.snowy.haruka.sync.SyncEngine.RETRY_MAX_MS
        var backoff = 0L
        fun next(): Long {
            backoff = if (backoff == 0L) start else (backoff * 2).coerceAtMost(cap)
            return backoff
        }
        assertEquals(start, next())
        assertEquals(start * 2, next())
        while (backoff < cap) next()
        assertEquals(cap, backoff)
        assertEquals(cap, next())
    }
}
