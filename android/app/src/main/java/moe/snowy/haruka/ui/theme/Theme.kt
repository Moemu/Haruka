package moe.snowy.haruka.ui.theme

import android.os.Build
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.dynamicDarkColorScheme
import androidx.compose.material3.dynamicLightColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext

// 品牌兜底调色板（spec §3.1）：灯塔暖琥珀 / 海雾蓝 / 珊瑚粉；
// 浅色底暖奶油、深色底深夜蓝。Android 12+ 优先动态取色。
private val LightColors = lightColorScheme(
    primary = Color(0xFF7C5800),
    onPrimary = Color(0xFFFFFFFF),
    primaryContainer = Color(0xFFFFDEA1),
    onPrimaryContainer = Color(0xFF271900),
    secondary = Color(0xFF4C617B),
    onSecondary = Color(0xFFFFFFFF),
    secondaryContainer = Color(0xFFD4E4FF),
    onSecondaryContainer = Color(0xFF041E32),
    tertiary = Color(0xFFA63A46),
    onTertiary = Color(0xFFFFFFFF),
    tertiaryContainer = Color(0xFFFFDAD8),
    onTertiaryContainer = Color(0xFF410006),
    background = Color(0xFFFFF8F2),
    onBackground = Color(0xFF221B13),
    surface = Color(0xFFFFF8F2),
    onSurface = Color(0xFF221B13),
    surfaceVariant = Color(0xFFF0E0CF),
    onSurfaceVariant = Color(0xFF4F4436),
    outline = Color(0xFF807467),
)

private val DarkColors = darkColorScheme(
    primary = Color(0xFFF7BD48),
    onPrimary = Color(0xFF402D00),
    primaryContainer = Color(0xFF5D4200),
    onPrimaryContainer = Color(0xFFFFDEA1),
    secondary = Color(0xFFB4C9E5),
    onSecondary = Color(0xFF243441),
    secondaryContainer = Color(0xFF3B4B5E),
    onSecondaryContainer = Color(0xFFD0E4FF),
    tertiary = Color(0xFFFFB3B2),
    onTertiary = Color(0xFF61121B),
    tertiaryContainer = Color(0xFF87252F),
    onTertiaryContainer = Color(0xFFFFDAD8),
    background = Color(0xFF1A130C),
    onBackground = Color(0xFFF1DFCC),
    surface = Color(0xFF1A130C),
    onSurface = Color(0xFFF1DFCC),
    surfaceVariant = Color(0xFF4F4436),
    onSurfaceVariant = Color(0xFFD3C4B4),
    outline = Color(0xFF9C8F80),
)

@Composable
fun HarukaTheme(
    darkTheme: Boolean = isSystemInDarkTheme(),
    content: @Composable () -> Unit,
) {
    val context = LocalContext.current
    val colorScheme = when {
        Build.VERSION.SDK_INT >= Build.VERSION_CODES.S ->
            if (darkTheme) dynamicDarkColorScheme(context) else dynamicLightColorScheme(context)
        darkTheme -> DarkColors
        else -> LightColors
    }
    MaterialTheme(colorScheme = colorScheme, content = content)
}
