package moe.snowy.haruka.ui

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.FilterChip
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.SegmentedButton
import androidx.compose.material3.SegmentedButtonDefaults
import androidx.compose.material3.SingleChoiceSegmentedButtonRow
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import moe.snowy.haruka.R
import moe.snowy.haruka.protocol.VALID_MOODS
import moe.snowy.haruka.protocol.endOfTodayUtcMs
import moe.snowy.haruka.protocol.validateManualMood
import moe.snowy.haruka.protocol.validateManualText

/**
 * 页面二：情绪 / 人格双入口（0.1.8，spec §4.2 插件化改版）。
 * 情绪 = 固定种子 chips/自定义 + 附言 + 有效期四档（mood 插件，限时）；
 * 人格 = 服务端封闭列表 choices chips + 附言，切换即长期接管，清除回落
 * 默认人格（persona 插件，无限期）。离线提交进队列如实显示。
 */
@OptIn(ExperimentalLayoutApi::class)
@Composable
fun ManualStatusScreen(vm: AppViewModel) {
    val settings by vm.settings.collectAsStateWithLifecycle()
    val personaChoices by vm.personaChoices.collectAsStateWithLifecycle()
    var mode by rememberSaveable { mutableStateOf(0) } // 0 = 情绪，1 = 人格
    var mood by rememberSaveable { mutableStateOf("") }
    var moodText by rememberSaveable { mutableStateOf("") }
    var expiryIndex by rememberSaveable { mutableStateOf(0) }
    var persona by rememberSaveable { mutableStateOf("") }
    var personaText by rememberSaveable { mutableStateOf("") }

    LaunchedEffect(Unit) { vm.loadPersonaChoices() }

    val moodError = validateManualMood(mood.trim())
    val moodTextError = validateManualText(moodText.trim())
    val personaTextError = validateManualText(personaText.trim())
    val codepoints = moodText.trim().codePointCount(0, moodText.trim().length)
    val canPublishMood = mood.isNotBlank() && moodError == null && moodTextError == null
    val canPublishPersona = persona.isNotBlank() && personaTextError == null

    LazyColumn(
        modifier = Modifier.fillMaxSize(),
        contentPadding = PaddingValues(16.dp),
        verticalArrangement = Arrangement.spacedBy(16.dp),
    ) {
        item {
            SingleChoiceSegmentedButtonRow(modifier = Modifier.fillMaxWidth()) {
                val modes = listOf(
                    stringResource(R.string.manual_mode_mood),
                    stringResource(R.string.manual_mode_persona),
                )
                modes.forEachIndexed { i, label ->
                    SegmentedButton(
                        selected = mode == i,
                        onClick = { mode = i },
                        shape = SegmentedButtonDefaults.itemShape(index = i, count = modes.size),
                    ) { Text(label) }
                }
            }
        }

        if (mode == 0) {
            // ---- 情绪（mood 插件，限时） ----
            item {
                CurrentMoodCard(settings)
            }
            item {
                Text(stringResource(R.string.manual_title), style = MaterialTheme.typography.titleLarge)
                FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    VALID_MOODS.forEach { m ->
                        FilterChip(
                            selected = mood == m,
                            onClick = { mood = if (mood == m) "" else m },
                            label = { Text(moodLabel(m) ?: m) },
                        )
                    }
                }
            }
            item {
                OutlinedTextField(
                    value = mood,
                    onValueChange = { mood = it },
                    label = { Text(stringResource(R.string.manual_mood_hint)) },
                    isError = moodError != null,
                    supportingText = { Text(moodError ?: "", style = MaterialTheme.typography.labelSmall) },
                    singleLine = true,
                    modifier = Modifier.fillMaxWidth(),
                )
            }
            item {
                NoteField(moodText, { moodText = it }, moodTextError, codepoints)
            }
            item {
                SingleChoiceSegmentedButtonRow(modifier = Modifier.fillMaxWidth()) {
                    val labels = listOf(
                        stringResource(R.string.manual_expiry_1h),
                        stringResource(R.string.manual_expiry_4h),
                        stringResource(R.string.manual_expiry_today),
                        stringResource(R.string.manual_expiry_indef),
                    )
                    labels.forEachIndexed { i, label ->
                        SegmentedButton(
                            selected = expiryIndex == i,
                            onClick = { expiryIndex = i },
                            shape = SegmentedButtonDefaults.itemShape(index = i, count = labels.size),
                        ) { Text(label) }
                    }
                }
            }
            item {
                val now = System.currentTimeMillis()
                // 长期（index 3）= 无限期，untilMs 0，payload 不带 until。
                val untilMs = when (expiryIndex) {
                    0 -> now + 3_600_000L
                    1 -> now + 4 * 3_600_000L
                    2 -> endOfTodayUtcMs(now)
                    else -> 0L
                }
                PublishClearButtons(
                    canPublish = canPublishMood,
                    onPublish = { vm.publishMood(mood, moodText, untilMs) },
                    showClear = !settings.manualCleared,
                    onClear = { vm.clearMood() },
                )
            }
        } else {
            // ---- 人格（persona 插件，服务端封闭列表，无限期接管） ----
            item {
                CurrentPersonaCard(settings)
            }
            item {
                Text(stringResource(R.string.persona_title), style = MaterialTheme.typography.titleLarge)
                Text(stringResource(R.string.persona_hint), style = MaterialTheme.typography.bodySmall)
                if (personaChoices.isEmpty()) {
                    Text(
                        stringResource(R.string.persona_choices_empty),
                        style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.error,
                    )
                }
                FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    personaChoices.forEach { name ->
                        FilterChip(
                            selected = persona == name,
                            onClick = { persona = if (persona == name) "" else name },
                            label = { Text(name) },
                        )
                    }
                }
            }
            item {
                NoteField(personaText, { personaText = it }, personaTextError, personaText.trim().codePointCount(0, personaText.trim().length))
            }
            item {
                PublishClearButtons(
                    canPublish = canPublishPersona,
                    onPublish = { vm.publishPersona(persona, personaText) },
                    showClear = !settings.personaCleared,
                    onClear = { vm.clearPersona() },
                )
            }
        }
    }
}

@Composable
private fun CurrentMoodCard(settings: moe.snowy.haruka.data.Settings) {
    // 当前生效情绪卡（本地视角；公开视角见仪表盘快照卡）。untilMs=0 表示无限期。
    if (!settings.manualCleared && (settings.manualUntilMs == 0L || settings.manualUntilMs > System.currentTimeMillis())) {
        Card(modifier = Modifier.fillMaxWidth()) {
            Column(modifier = Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(4.dp)) {
                val label = moodLabel(settings.manualMood) ?: settings.manualMood
                val parts = listOfNotNull(label, settings.manualText.takeIf { it.isNotBlank() })
                Text(parts.joinToString(" · "), style = MaterialTheme.typography.bodyLarge)
                Text(
                    if (settings.manualUntilMs == 0L) stringResource(R.string.manual_active_indef)
                    else stringResource(R.string.manual_active_until, shortDateTime(settings.manualUntilMs)),
                    style = MaterialTheme.typography.bodySmall,
                )
            }
        }
    }
}

@Composable
private fun CurrentPersonaCard(settings: moe.snowy.haruka.data.Settings) {
    if (!settings.personaCleared && settings.personaName.isNotBlank()) {
        Card(modifier = Modifier.fillMaxWidth()) {
            Column(modifier = Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(4.dp)) {
                Text(
                    stringResource(R.string.persona_taking_over, settings.personaName),
                    style = MaterialTheme.typography.bodyLarge,
                )
                settings.personaText.takeIf { it.isNotBlank() }?.let {
                    Text(it, style = MaterialTheme.typography.bodyMedium)
                }
                Text(stringResource(R.string.manual_active_indef), style = MaterialTheme.typography.bodySmall)
            }
        }
    }
}

@Composable
private fun NoteField(value: String, onChange: (String) -> Unit, error: String?, codepoints: Int) {
    OutlinedTextField(
        value = value,
        onValueChange = onChange,
        label = { Text(stringResource(R.string.manual_note_hint)) },
        isError = error != null,
        supportingText = {
            Text(error ?: stringResource(R.string.manual_char_count, codepoints), style = MaterialTheme.typography.labelSmall)
        },
        modifier = Modifier.fillMaxWidth(),
    )
}

@Composable
private fun PublishClearButtons(canPublish: Boolean, onPublish: () -> Unit, showClear: Boolean, onClear: () -> Unit) {
    Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
        Button(onClick = onPublish, enabled = canPublish, modifier = Modifier.fillMaxWidth()) {
            Text(stringResource(R.string.manual_publish))
        }
        if (showClear) {
            OutlinedButton(onClick = onClear, modifier = Modifier.fillMaxWidth()) {
                Text(stringResource(R.string.manual_clear))
            }
        }
    }
}
