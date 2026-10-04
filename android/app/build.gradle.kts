import java.time.ZoneOffset
import java.time.ZonedDateTime
import java.time.format.DateTimeFormatter

plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.plugin.compose")
    id("org.jetbrains.kotlin.plugin.serialization")
    id("com.google.devtools.ksp")
    id("androidx.room")
}

// Every build carries its UTC time, visible in the dashboard (Devices): which phone runs which build.
// A release sets zweep.version and zweep.versionCode instead.
val buildStamp: ZonedDateTime = ZonedDateTime.now(ZoneOffset.UTC)

android {
    namespace = "net.nicodroid.zweep"
    compileSdk = 37

    defaultConfig {
        applicationId = "net.nicodroid.zweep"
        minSdk = 29
        targetSdk = 37
        versionCode = providers.gradleProperty("zweep.versionCode").map { it.toInt() }
            .getOrElse(buildStamp.format(DateTimeFormatter.ofPattern("yyMMddHH")).toInt())
        versionName = providers.gradleProperty("zweep.version")
            .getOrElse("0.8.0-dev+" + buildStamp.format(DateTimeFormatter.ofPattern("yyyyMMdd.HHmm")))
        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
        buildConfigField("String", "SOURCE_URL", "\"${providers.gradleProperty("zweep.sourceUrl").getOrElse("")}\"")
    }

    buildFeatures {
        compose = true
        buildConfig = true
    }

    // Release signing: the keystore stays outside the repository (android/signing/README.md). Every
    // release must be signed with the same key, or phones refuse the update.
    val keystore = providers.environmentVariable("ZWEEP_KEYSTORE").orNull
    signingConfigs {
        if (keystore != null) {
            create("release") {
                storeFile = file(keystore)
                storePassword = providers.environmentVariable("ZWEEP_KEYSTORE_PASSWORD").orNull
                keyAlias = providers.environmentVariable("ZWEEP_KEY_ALIAS").getOrElse("zweep")
                keyPassword = providers.environmentVariable("ZWEEP_KEY_PASSWORD").orElse(providers.environmentVariable("ZWEEP_KEYSTORE_PASSWORD")).orNull
                enableV1Signing = false
                enableV2Signing = true
                enableV3Signing = true
            }
        }
    }

    buildTypes {
        release {
            if (keystore != null) signingConfig = signingConfigs.getByName("release")
            isMinifyEnabled = true
            isShrinkResources = true
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    androidResources {
        localeFilters += listOf("en", "it")
        generateLocaleConfig = true
    }

    // F-Droid and reproducible builds: no Google-encrypted dependency metadata in the APK
    dependenciesInfo {
        includeInApk = false
        includeInBundle = false
    }

    testOptions {
        unitTests.isReturnDefaultValues = true
    }
}

room {
    schemaDirectory("$projectDir/schemas")
}

dependencies {
    val composeBom = platform("androidx.compose:compose-bom:2026.09.00")
    implementation(composeBom)
    implementation("androidx.compose.ui:ui")
    implementation("androidx.compose.material3:material3")
    implementation("androidx.compose.ui:ui-tooling-preview")
    debugImplementation("androidx.compose.ui:ui-tooling")
    implementation("androidx.activity:activity-compose:1.13.0")
    implementation("androidx.lifecycle:lifecycle-viewmodel-compose:2.11.0")
    implementation("androidx.lifecycle:lifecycle-runtime-compose:2.11.0")
    implementation("androidx.room:room-runtime:2.8.5")
    implementation("androidx.room:room-ktx:2.8.5")
    ksp("androidx.room:room-compiler:2.8.5")
    implementation("androidx.browser:browser:1.8.0")
    implementation("com.squareup.okhttp3:okhttp:5.5.0")
    implementation("org.jetbrains.kotlinx:kotlinx-serialization-json:1.8.1")
    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-android:1.11.0")

    testImplementation("junit:junit:4.13.2")
    testImplementation("org.jetbrains.kotlinx:kotlinx-coroutines-test:1.11.0")
}
