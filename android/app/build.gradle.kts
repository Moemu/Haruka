import java.util.Properties

plugins {
    alias(libs.plugins.android.application)
    alias(libs.plugins.kotlin.android)
    alias(libs.plugins.kotlin.compose)
    alias(libs.plugins.kotlin.serialization)
}

// 签名密钥双源（spec §7）：CI 走环境变量（KEYSTORE_FILE/KEYSTORE_PASSWORD/KEY_ALIAS/KEY_PASSWORD，
// keystore 由 workflow 从 Secret 解码），本地走 android/keystore.properties（gitignored）。
// 两者都没有时 release 构建在签名校验处失败，不产出 unsigned 包。
val keystoreProperties = Properties().apply {
    rootProject.file("keystore.properties").takeIf { it.exists() }?.inputStream()?.use { load(it) }
}

fun signingEnv(name: String): String? = System.getenv(name)?.takeIf { it.isNotBlank() }

val releaseStoreFile = signingEnv("KEYSTORE_FILE")?.let { rootProject.file(it) }
    ?: keystoreProperties.getProperty("storeFile")?.let { rootProject.file(it) }
val releaseStorePassword = signingEnv("KEYSTORE_PASSWORD") ?: keystoreProperties.getProperty("storePassword")
val releaseKeyAlias = signingEnv("KEY_ALIAS") ?: keystoreProperties.getProperty("keyAlias")
val releaseKeyPassword = signingEnv("KEY_PASSWORD") ?: keystoreProperties.getProperty("keyPassword")

android {
    namespace = "moe.snowy.haruka"
    compileSdk = 35

    defaultConfig {
        applicationId = "moe.snowy.haruka"
        minSdk = 29
        targetSdk = 35
        versionCode = 7
        versionName = "0.1.6"
    }

    signingConfigs {
        create("release") {
            if (releaseStoreFile != null && releaseStoreFile.exists() &&
                releaseStorePassword != null && releaseKeyAlias != null && releaseKeyPassword != null
            ) {
                storeFile = releaseStoreFile
                storePassword = releaseStorePassword
                keyAlias = releaseKeyAlias
                keyPassword = releaseKeyPassword
                enableV1Signing = false
                enableV2Signing = true
                enableV3Signing = true
            }
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = true
            isShrinkResources = true
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
            signingConfig = signingConfigs.getByName("release")
        }
    }
    buildFeatures {
        buildConfig = true
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }
}

dependencies {
    implementation(libs.androidx.core.ktx)
    implementation(libs.androidx.activity.compose)
    implementation(platform(libs.androidx.compose.bom))
    implementation(libs.androidx.compose.ui)
    implementation(libs.androidx.compose.material3)
    implementation(libs.androidx.navigation.compose)
    implementation(libs.androidx.lifecycle.runtime.compose)
    implementation(libs.androidx.lifecycle.viewmodel.compose)
    implementation(libs.androidx.datastore.preferences)
    implementation(libs.androidx.work.runtime.ktx)
    implementation(libs.okhttp)
    implementation(libs.kotlinx.serialization.json)
    implementation(libs.kotlinx.coroutines.android)

    testImplementation(libs.kotlin.test)
}
