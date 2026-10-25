package moe.snowy.haruka.location

import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import moe.snowy.haruka.protocol.LocationData
import moe.snowy.haruka.protocol.SOURCE_GPS_COARSE
import moe.snowy.haruka.protocol.ZONE_HOME

/**
 * 离线行政区划反解引擎（方案 B 核心，spec §6.3 B）：
 * 坐标只进内存，产出行政区名即弃；边界包由 cmd/boundaries 构建（GeoBoundaries 几何
 * + DataV 区划名表，构建期已预算好区县所属的市/省名）。
 *
 * 精度规则：点落在县内且距县界 > 3km → district；距县界 ≤ 3km → 降级 city（不硬猜）；
 * 不在任何县内 → 回退省级；连省都不中 → null（如实未知）。
 */
class BoundaryLookup private constructor(
    private val adm1: List<Area>,
    /** 区县级区域列表（只读，供诊断与测试）。 */
    val districts: List<Area>,
) {
    private val adm3: List<Area> = districts
    @Serializable
    private data class Bundle(val adm1: List<AreaDto> = emptyList(), val adm3: List<AreaDto> = emptyList())

    @Serializable
    private data class AreaDto(
        val n: String,
        val c: String? = null,
        val p: String? = null,
        val b: List<Double> = emptyList(),
        val r: List<List<Double>> = emptyList(),
    )

    class Area internal constructor(
        val name: String,
        val city: String?,
        val province: String?,
        val bbox: DoubleArray, // [minLon, minLat, maxLon, maxLat]
        val rings: List<FloatArray>, // 扁平 [lon0, lat0, lon1, lat1, ...]
    )

    data class Resolved(
        val province: String?,
        val city: String?,
        val district: String?,
        val precision: String,
    )

    /** 主入口：lat/lon（WGS-84）→ 反解结果；无法确定时返回 null。 */
    fun lookup(lat: Double, lon: Double): Resolved? {
        // 数据源存在少量重叠多边形：取「包含最深」的（点距其边界最远），最确定
        val adm3Hit = adm3.filter { it.contains(lat, lon) }
            .maxByOrNull { it.boundaryDistanceKm(lat, lon) }
        if (adm3Hit != null) {
            val distKm = adm3Hit.boundaryDistanceKm(lat, lon)
            return if (distKm > BUFFER_KM) {
                Resolved(adm3Hit.province, adm3Hit.city, adm3Hit.name, PRECISION_DISTRICT)
            } else {
                // 距县界 3km 内：降级市级，district 留空（不硬猜）
                Resolved(adm3Hit.province, adm3Hit.city, null, PRECISION_CITY)
            }
        }
        val adm1Hit = adm1.filter { it.contains(lat, lon) }
            .maxByOrNull { it.boundaryDistanceKm(lat, lon) }
        if (adm1Hit != null) {
            return Resolved(adm1Hit.name, null, null, PRECISION_PROVINCE)
        }
        return null
    }

    /** 诊断用：返回判定所依据的多边形边界距离（km）；无命中时 null。 */
    fun debugBoundaryKm(lat: Double, lon: Double): Double? {
        val adm3Hits = adm3.filter { it.contains(lat, lon) }
        val best = adm3Hits.maxByOrNull { it.boundaryDistanceKm(lat, lon) }
        return best?.boundaryDistanceKm(lat, lon)
    }

    /** 在家判定（c-3）：lat/lon 是否落在指定区名内（区级命中即 home）。 */
    fun isHomeDistrict(lat: Double, lon: Double, homeDistrict: String): Boolean {
        if (homeDistrict.isBlank()) return false
        return adm3.any { it.name == homeDistrict.trim() && it.contains(lat, lon) }
    }

    private fun Area.contains(lat: Double, lon: Double): Boolean {
        if (lon < bbox[0] || lat < bbox[1] || lon > bbox[2] || lat > bbox[3]) return false
        var inside = false
        for (r in rings) {
            var j = r.size - 2
            var i = 0
            while (i + 1 < r.size) {
                val xi = r[i].toDouble(); val yi = r[i + 1].toDouble()
                val xj = r[j].toDouble(); val yj = r[j + 1].toDouble()
                if ((yi > lat) != (yj > lat)) {
                    if (lon < (xj - xi) * (lat - yi) / (yj - yi) + xi) {
                        inside = !inside
                    }
                }
                j = i
                i += 2
            }
        }
        return inside
    }

    /** 点到该区域边界的最短距离（km，等距圆柱近似，低频采样下精度足够）。 */
    private fun Area.boundaryDistanceKm(lat: Double, lon: Double): Double {
        val kmPerDegLat = 110.574
        val kmPerDegLon = 111.320 * Math.cos(Math.toRadians(lat))
        var best = Double.MAX_VALUE
        for (r in rings) {
            var j = r.size - 2
            var i = 0
            while (i + 1 < r.size) {
                val ax = r[i].toDouble(); val ay = r[i + 1].toDouble()
                val bx = r[j].toDouble(); val by = r[j + 1].toDouble()
                // 转成 km 平面后再做点到线段距离
                val axKm = ax * kmPerDegLon; val ayKm = ay * kmPerDegLat
                val bxKm = bx * kmPerDegLon; val byKm = by * kmPerDegLat
                val pKm = lon * kmPerDegLon; val pLatKm = lat * kmPerDegLat
                val abx = bxKm - axKm; val aby = byKm - ayKm
                val apx = pKm - axKm; val apy = pLatKm - ayKm
                val ab2 = abx * abx + aby * aby
                var t = if (ab2 == 0.0) 0.0 else (apx * abx + apy * aby) / ab2
                t = t.coerceIn(0.0, 1.0)
                val dx = apx - t * abx; val dy = apy - t * aby
                val d2 = dx * dx + dy * dy
                if (d2 < best) best = d2
                j = i
                i += 2
            }
        }
        return Math.sqrt(best)
    }

    companion object {
        const val PRECISION_DISTRICT = "district"
        const val PRECISION_CITY = "city"
        const val PRECISION_PROVINCE = "province"
        const val BUFFER_KM = 3.0

        private val json = Json { ignoreUnknownKeys = true }

        /**
         * 从边界包构建；gzip 或明文均可（AGP 会把 assets 里的 .gz 解压并去掉后缀，
         * APK 内实际是明文 JSON）；IO 密集，调用方自行放到后台线程。
         */
        fun load(input: java.io.InputStream): BoundaryLookup {
            val bytes = input.buffered().readBytes()
            val gzipped = bytes.size >= 2 && bytes[0] == 0x1f.toByte() && bytes[1] == 0x8b.toByte()
            val text = if (gzipped) {
                java.util.zip.GZIPInputStream(bytes.inputStream()).use { it.readBytes().decodeToString() }
            } else {
                bytes.decodeToString()
            }
            val bundle = json.decodeFromString(Bundle.serializer(), text)
            return BoundaryLookup(
                bundle.adm1.map { it.toArea() },
                bundle.adm3.map { it.toArea() },
            )
        }

        private fun AreaDto.toArea(): Area = Area(
            name = n,
            city = c,
            province = p,
            bbox = doubleArrayOf(b.getOrElse(0) { 0.0 }, b.getOrElse(1) { 0.0 }, b.getOrElse(2) { 0.0 }, b.getOrElse(3) { 0.0 }),
            rings = r.map { ring -> FloatArray(ring.size) { i -> ring[i].toFloat() } },
        )
    }
}

/**
 * 采样坐标 → 位置事件负载：反解失败返回 null（设备端保持静默，不回退方案 A），
 * 坐标本身不进入返回值。zone 判定在此收敛（c-3 在家；后续公司/学校等地点只扩这里）：
 * 家区名为空或采样点未落入时 zone 留空。判定粒度为区县（边界包 ADM3），
 * 与家同区县的地点无法区分——用户已确认可接受（design.md §6.3 zone 粗粒度口径）。
 */
fun locationData(
    lookup: BoundaryLookup,
    lat: Double,
    lon: Double,
    homeDistrict: String,
): LocationData? {
    val r = lookup.lookup(lat, lon) ?: return null
    return LocationData(
        source = SOURCE_GPS_COARSE,
        precision = r.precision,
        province = r.province,
        city = r.city,
        district = r.district,
        zone = if (homeDistrict.isNotBlank() && lookup.isHomeDistrict(lat, lon, homeDistrict)) ZONE_HOME else null,
    )
}
