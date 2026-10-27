package moe.snowy.haruka

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Face
import androidx.compose.material.icons.filled.Home
import androidx.compose.material.icons.filled.PlayArrow
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material3.ExtendedFloatingActionButton
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.NavigationBar
import androidx.compose.material3.NavigationBarItem
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableLongStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.viewmodel.compose.viewModel
import androidx.navigation.NavGraph.Companion.findStartDestination
import androidx.navigation.compose.NavHost
import androidx.navigation.compose.composable
import androidx.navigation.compose.currentBackStackEntryAsState
import androidx.navigation.compose.rememberNavController
import kotlinx.coroutines.delay
import moe.snowy.haruka.ui.AppViewModel
import moe.snowy.haruka.ui.DashboardScreen
import moe.snowy.haruka.ui.KeepAliveScreen
import moe.snowy.haruka.ui.LogScreen
import moe.snowy.haruka.ui.ManualStatusScreen
import moe.snowy.haruka.ui.OnboardingScreen
import moe.snowy.haruka.ui.SettingsAboutPage
import moe.snowy.haruka.ui.SettingsCollectPage
import moe.snowy.haruka.ui.SettingsControlPage
import moe.snowy.haruka.ui.SettingsScreen
import moe.snowy.haruka.ui.SettingsServerPage
import moe.snowy.haruka.ui.durationText
import moe.snowy.haruka.ui.theme.HarukaTheme

class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        setContent {
            HarukaTheme {
                HarukaRoot()
            }
        }
    }
}

@Composable
fun HarukaRoot(vm: AppViewModel = viewModel(factory = AppViewModel.Factory)) {
    val settings by vm.settings.collectAsStateWithLifecycle()
    val navController = rememberNavController()
    NavHost(navController, startDestination = if (settings.configured) "main" else "onboarding") {
        composable("onboarding") {
            OnboardingScreen(
                vm = vm,
                onDone = {
                    navController.navigate("main") { popUpTo(0) { inclusive = true } }
                },
            )
        }
        composable("main") { MainScreen(vm) }
    }
}

@Composable
fun MainScreen(vm: AppViewModel) {
    val navController = rememberNavController()
    val backStack by navController.currentBackStackEntryAsState()
    val current = backStack?.destination?.route ?: "dashboard"
    val runtime by vm.runtime.collectAsStateWithLifecycle()

    // 操作反馈统一走 Snackbar：家区县保存、定位策略确认、暂停/隐身、手动状态发布等
    val snackbarHostState = remember { SnackbarHostState() }
    val message by vm.message.collectAsStateWithLifecycle()
    LaunchedEffect(message) {
        message?.let {
            snackbarHostState.showSnackbar(it)
            vm.consumeMessage()
        }
    }

    Scaffold(
        snackbarHost = { SnackbarHost(snackbarHostState) },
        // 服务总开关固定在右下角（FlClash 式 Extended FAB）：图标旁带文本——
        // 停止时「启动」，运行时「已运行 x」，操作意图与状态一眼可见
        floatingActionButton = {
            if (current == "dashboard") {
                val running = runtime.running
                var nowMs by remember { mutableLongStateOf(System.currentTimeMillis()) }
                LaunchedEffect(running) {
                    while (running) {
                        delay(30_000)
                        nowMs = System.currentTimeMillis()
                    }
                }
                ExtendedFloatingActionButton(
                    text = {
                        Text(
                            if (running) stringResource(R.string.fab_running, durationText(runtime.startedAtMs, nowMs))
                            else stringResource(R.string.fab_start),
                        )
                    },
                    icon = {
                        if (running) {
                            val stopLabel = stringResource(R.string.action_stop_service)
                            Box(
                                modifier = Modifier
                                    .size(20.dp)
                                    .background(MaterialTheme.colorScheme.onPrimaryContainer, RoundedCornerShape(3.dp))
                                    .semantics { contentDescription = stopLabel },
                            )
                        } else {
                            Icon(Icons.Filled.PlayArrow, contentDescription = null)
                        }
                    },
                    onClick = { if (running) vm.stopService() else vm.startService() },
                )
            }
        },
        bottomBar = {
            NavigationBar {
                TopLevelDestination.items.forEach { item ->
                    NavigationBarItem(
                        selected = current == item.route,
                        onClick = {
                            navController.navigate(item.route) {
                                popUpTo(navController.graph.findStartDestination().id) { saveState = true }
                                launchSingleTop = true
                                restoreState = true
                            }
                        },
                        icon = { Icon(item.icon, contentDescription = null) },
                        label = { Text(item.labelRes.let { androidx.compose.ui.res.stringResource(it) }) },
                    )
                }
            }
        },
    ) { padding ->
        NavHost(
            navController = navController,
            startDestination = "dashboard",
            modifier = Modifier.padding(padding),
        ) {
            composable("dashboard") { DashboardScreen(vm, onOpenKeepAlive = { navController.navigate("keepalive") }) }
            composable("manual") { ManualStatusScreen(vm) }
            composable("settings") {
                SettingsScreen(
                    vm,
                    onOpenServer = { navController.navigate("settings/server") },
                    onOpenCollect = { navController.navigate("settings/collect") },
                    onOpenControl = { navController.navigate("settings/control") },
                    onOpenAbout = { navController.navigate("settings/about") },
                    onOpenKeepAlive = { navController.navigate("keepalive") },
                    onOpenLogs = { navController.navigate("logs") },
                )
            }
            composable("settings/server") { SettingsServerPage(vm, onBack = { navController.popBackStack() }) }
            composable("settings/collect") { SettingsCollectPage(vm, onBack = { navController.popBackStack() }) }
            composable("settings/control") { SettingsControlPage(vm, onBack = { navController.popBackStack() }) }
            composable("settings/about") { SettingsAboutPage(onBack = { navController.popBackStack() }) }
            composable("keepalive") { KeepAliveScreen(vm, onBack = { navController.popBackStack() }) }
            composable("logs") { LogScreen(vm, onBack = { navController.popBackStack() }) }
        }
    }
}

private enum class TopLevelDestination(val route: String, val icon: androidx.compose.ui.graphics.vector.ImageVector, val labelRes: Int) {
    Dashboard("dashboard", Icons.Filled.Home, R.string.nav_dashboard),
    Manual("manual", Icons.Filled.Face, R.string.nav_manual),
    Settings("settings", Icons.Filled.Settings, R.string.nav_settings),
    ;

    companion object {
        val items = entries
    }
}
