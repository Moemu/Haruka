package moe.snowy.haruka

import moe.snowy.haruka.location.BoundaryLookup
import moe.snowy.haruka.location.locationData
import moe.snowy.haruka.protocol.LocationData
import moe.snowy.haruka.protocol.SOURCE_GPS_COARSE
import moe.snowy.haruka.protocol.Wire
import java.io.File
import java.io.FileInputStream
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNotNull
import kotlin.test.assertNull
import kotlin.test.assertTrue
import kotlin.test.fail

/** 离线反解引擎测试：真实边界包（assets）上的已知坐标断言与 3km 缓冲规则验证。 */
class BoundaryLookupTest {
    private val lookup: BoundaryLookup by lazy {
        val f = locateAssets().resolve("haruka-boundaries.json.gz")
        BoundaryLookup.load(FileInputStream(f))
    }

    /** 从测试工作目录向上找 android/app/src/main/assets。 */
    private fun locateAssets(): File {
        var dir = File(System.getProperty("user.dir"))
        repeat(6) {
            val candidate = File(dir, "app/src/main/assets")
            if (candidate.isDirectory) return candidate
            val alt = File(dir, "android/app/src/main/assets")
            if (alt.isDirectory) return alt
            dir = dir.parentFile ?: dir
        }
        fail("找不到 assets 目录（工作目录 ${System.getProperty("user.dir")}）")
    }

    @Test
    fun `deep inland point resolves to district precision`() {
        // 鹿寨县城（深入县内，距简化边界远大于 3km）
        val r = lookup.lookup(24.483405, 109.740805)
        assertNotNull(r)
        assertEquals(BoundaryLookup.PRECISION_DISTRICT, r.precision)
        assertEquals("鹿寨县", r.district)
        assertEquals("柳州市", r.city)
        assertEquals("广西壮族自治区", r.province)
    }

    @Test
    fun `point near simplified boundary degrades to city`() {
        // 徐汇区内近黄浦江一侧的点：距简化边界 <3km，按规则降级市级
        val r = lookup.lookup(31.1885, 121.4372)
        assertNotNull(r)
        assertEquals(BoundaryLookup.PRECISION_CITY, r.precision)
        assertNull(r.district)
        assertEquals("上海市", r.city)
    }

    @Test
    fun `buffer rule is self consistent with chosen polygon distance`() {
        // 规则自洽：precision == city ⇔ 判定多边形边界距离 ≤ 3km
        val samples = listOf(
            24.483405 to 109.740805, // 鹿寨
            31.1885 to 121.4372,     // 徐汇近界
            30.2590 to 120.1200,     // 杭州
            39.956074 to 116.310316, // 海淀
            23.12 to 113.36,         // 广州
            39.82 to 109.78,         // 鄂尔多斯
        )
        for ((lat, lon) in samples) {
            val r = lookup.lookup(lat, lon)
            val d = lookup.debugBoundaryKm(lat, lon)
            assertNotNull(r)
            assertNotNull(d)
            assertEquals(
                (r.precision == BoundaryLookup.PRECISION_CITY),
                d <= BoundaryLookup.BUFFER_KM,
                "($lat,$lon) precision=${r.precision} dist=$d",
            )
        }
    }

    @Test
    fun `offshore point resolves to null not guess`() {
        assertNull(lookup.lookup(28.0, 125.0), "东海开阔水域不应命中任何行政区")
    }

    @Test
    fun `province fallback when outside all districts`() {
        val r = lookup.lookup(43.5, 115.5)
        assertNotNull(r, "内蒙古腹地至少应命中省级")
        assertNotNull(r.province)
    }

    @Test
    fun `lookup is fast enough for low frequency sampling`() {
        val start = System.currentTimeMillis()
        repeat(20) { i ->
            lookup.lookup(30.0 + i * 0.01, 120.0 + i * 0.01)
        }
        val elapsed = System.currentTimeMillis() - start
        assertTrue(elapsed < 3000, "20 次查询应在 3s 内完成（实际 ${elapsed}ms）")
    }

    @Test
    fun `locationData assembles wire payload without coordinates`() {
        val d = locationData(lookup, 24.483405, 109.740805, "")
        assertNotNull(d)
        assertEquals(SOURCE_GPS_COARSE, d.source)
        assertEquals(BoundaryLookup.PRECISION_DISTRICT, d.precision)
        assertEquals("鹿寨县", d.district)
        assertEquals("柳州市", d.city)
        assertEquals("广西壮族自治区", d.province)
        assertNull(d.zone)
        // 序列化形态不得携带任何坐标或多余字段（坐标不出内存的落点断言）
        val json = Wire.json.encodeToString(LocationData.serializer(), d)
        assertFalse(json.contains("109.74"))
        assertFalse(json.contains("24.48"))
        assertTrue(json.contains("\"source\":\"gps_coarse\""))
    }

    @Test
    fun `locationData is null offshore - silent not fallback`() {
        assertNull(locationData(lookup, 28.0, 125.0, ""))
    }

    @Test
    fun `zone home attaches only when point falls in configured district`() {
        // 命中：采样点落入所配家区 → zone=home，district 仍填真实区名（design.md §6.3）
        val home = locationData(lookup, 24.483405, 109.740805, "鹿寨县")
        assertNotNull(home)
        assertEquals("home", home.zone)
        assertEquals("鹿寨县", home.district)
        val json = Wire.json.encodeToString(LocationData.serializer(), home)
        assertTrue(json.contains("\"zone\":\"home\""))

        // 未命中：家区配置了但采样点在别处 → zone 留空
        val away = locationData(lookup, 31.1885, 121.4372, "鹿寨县")
        assertNotNull(away)
        assertNull(away.zone)

        // 同名区靠包含判定：柳州城中区的点配「城中区」也命中（区名可跨市重名）。
        // zone 只看包含关系，独立于 3km 缓冲的精度降级（该点距简化边界近时 district 为空）。
        val liuzhou = locationData(lookup, 24.3190, 109.4155, "城中区")
        assertNotNull(liuzhou)
        assertEquals("home", liuzhou.zone)
        assertTrue(liuzhou.precision in setOf("district", "city"))

        // 家区为空 → 永不附加
        val unset = locationData(lookup, 24.483405, 109.740805, "")
        assertNotNull(unset)
        assertNull(unset.zone)

        // 学区命中：家/学校各配一区，采样点落入学区 → zone=school（与家异区县，互斥）
        val school = locationData(lookup, 24.3190, 109.4155, "鹿寨县", "城中区")
        assertNotNull(school)
        assertEquals("school", school.zone)

        // 家区优先不变：落入家区的点即使学区也配置，仍是 home
        val bothHome = locationData(lookup, 24.483405, 109.740805, "鹿寨县", "城中区")
        assertNotNull(bothHome)
        assertEquals("home", bothHome.zone)
    }
}
