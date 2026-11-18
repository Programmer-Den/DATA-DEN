import org.jetbrains.kotlin.gradle.dsl.JvmTarget

plugins {
    alias(libs.plugins.android.application)
    alias(libs.plugins.kotlin.android)
}

// The release, such as 1.2.3, from the Release workflow (-PappVersion=1.2.3);
// without it the app calls itself "dev".
val appVersion: String? = providers.gradleProperty("appVersion").orNull
if (appVersion != null && !appVersion.matches(Regex("""(0|[1-9][0-9]{0,2})\.(0|[1-9][0-9]{0,2})\.(0|[1-9][0-9]{0,2})"""))) {
    throw GradleException("appVersion '$appVersion' is not a version such as 1.2.3 (each part below 1000)")
}

android {
    namespace = "io.github.programmerden.dataden"
    compileSdk = 36

    defaultConfig {
        applicationId = "io.github.programmerden.dataden"
        // Android 10: scoped storage and MediaStore.setRequireOriginal.
        minSdk = 29
        targetSdk = 36
        // 1.2.3 is 1002003, so every release has a higher code than the last.
        versionCode = appVersion?.split(".")?.map(String::toInt)?.let { (major, minor, patch) ->
            maxOf(1, major * 1_000_000 + minor * 1_000 + patch)
        } ?: 1
        versionName = appVersion ?: "dev"
    }

    buildTypes {
        release {
            isMinifyEnabled = false
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    lint {
        abortOnError = true
        checkDependencies = true
        // New versions come through Dependabot, not through a failing build.
        disable += setOf("GradleDependency", "AndroidGradlePluginVersion", "NewerVersionAvailable", "OldTargetApi")
    }
}

kotlin {
    compilerOptions {
        jvmTarget.set(JvmTarget.JVM_17)
    }
}

dependencies {
    implementation(project(":core"))
    implementation(libs.work.runtime)
}
