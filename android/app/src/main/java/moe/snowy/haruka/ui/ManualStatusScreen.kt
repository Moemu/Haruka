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
import androidx.compose.material3.Card
import androidx.compose.material3.FilterChip
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.SegmentedButton
import androidx.compose.material3.SegmentedButtonDefaults
import androidx.compose.material3.SingleChoiceSegmentedButtonRow
import androidx.compose.material3.Text
import androidx.compose.material3.Button
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import moe.snowy.haruka.R
import moe.snowy.haruka.protocol.VALID_MOODS
import moe.snowy.haruka.protocol.endOfTodayUtcMs
import moe.snowy.haruka.protocol.validateManualText

/** 页面二：手动状态（spec §4.2）：心情 chip 组 + 附言 + 有效期；离线提交进队列如实显示。 */
@OptIn(ExperimentalLayoutApi::class)
@Composable
fun ManualStatusScreen(vm: AppViewModel) {
    val settings by vm.settings.collectAsStateWithLifecycle()
    var mood by rememberSaveable { mutableStateOf("") }
    var text by rememberSaveable { mutableStateOf("") }
    var expiryIndex by rememberSaveable { mutableStateOf(0) }

    val textError = validateManualText(text.trim())
    val codepoints = text.trim().codePointCount(0, text.trim().length)
    val canPublish = mood.isNotBlank() && textError == null

    LazyColumn(
        modifier = Modifier.fillMaxSize(),
        contentPadding = PaddingValues(16.dp),
        verticalArrangement = Arrangement.spacedBy(16.dp),
    ) {
        item {
            // 当前生效状态卡（本地视角；公开视角见仪表盘快照卡）
            if (!settings.manualCleared && settings.manualUntilMs > System.currentTimeMillis()) {
                Card(modifier = Modifier.fillMaxWidth()) {
                    Column(modifier = Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(4.dp)) {
                        val label = moodLabel(settings.manualMood)
                        val parts = listOfNotNull(label, settings.manualText.takeIf { it.isNotBlank() })
                        Text(parts.joinToString(" · "), style = MaterialTheme.typography.bodyLarge)
                        Text(
                            stringResource(R.string.manual_active_until, shortDateTime(settings.manualUntilMs)),
                            style = MaterialTheme.typography.bodySmall,
                        )
                    }
                }
            }
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
                value = text,
                onValueChange = { text = it },
                label = { Text(stringResource(R.string.manual_note_hint)) },
                isError = textError != null,
                supportingText = {
                    Text(
                        textError ?: stringResource(R.string.manual_char_count, codepoints),
                        style = MaterialTheme.typography.labelSmall,
                    )
                },
                modifier = Modifier.fillMaxWidth(),
            )
        }
        item {
            SingleChoiceSegmentedButtonRow(modifier = Modifier.fillMaxWidth()) {
                val labels = listOf(
                    stringResource(R.string.manual_expiry_1h),
                    stringResource(R.string.manual_expiry_4h),
                    stringResource(R.string.manual_expiry_today),
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
            val untilMs = when (expiryIndex) {
                0 -> now + 3_600_000L
                1 -> now + 4 * 3_600_000L
                else -> endOfTodayUtcMs(now)
            }
            Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                Button(
                    onClick = { vm.publishManual(mood, text, untilMs) },
                    enabled = canPublish,
                    modifier = Modifier.fillMaxWidth(),
                ) { Text(stringResource(R.string.manual_publish)) }
                if (!settings.manualCleared) {
                    OutlinedButton(
                        onClick = { vm.clearManual() },
                        modifier = Modifier.fillMaxWidth(),
                    ) { Text(stringResource(R.string.manual_clear)) }
                }
            }
        }
    }
}
