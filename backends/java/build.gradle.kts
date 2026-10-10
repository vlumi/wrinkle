// The Java backend: one binary, roles chosen by configuration, built on
// JDK 25 with virtual threads; Armeria over Netty for the WebSocket server.

plugins {
    application
    id("com.google.protobuf") version "0.10.0"
    id("com.diffplug.spotless") version "8.10.4"
}

group = "wrinkle"

java {
    toolchain {
        languageVersion = JavaLanguageVersion.of(25)
    }
}

repositories {
    mavenCentral()
}

val armeriaVersion = "1.42.0"
val protobufVersion = "4.36.2"
val prometheusVersion = "1.9.0"

dependencies {
    implementation("com.linecorp.armeria:armeria:$armeriaVersion")
    implementation("com.linecorp.armeria:armeria-prometheus1:$armeriaVersion")
    implementation("com.google.protobuf:protobuf-java:$protobufVersion")
    implementation("com.mysql:mysql-connector-j:26.7.0")
    implementation("com.zaxxer:HikariCP:7.1.0")
    implementation("org.apache.kafka:kafka-clients:4.3.1")
    implementation("io.lettuce:lettuce-core:7.8.0.RELEASE")
    implementation("com.fasterxml.jackson.core:jackson-databind:2.22.3")
    implementation("io.prometheus:prometheus-metrics-core:$prometheusVersion")
    implementation("io.prometheus:prometheus-metrics-instrumentation-jvm:$prometheusVersion")
    implementation("org.slf4j:slf4j-api:2.0.20")
    runtimeOnly("org.slf4j:slf4j-simple:2.0.20")

    testImplementation(platform("org.junit:junit-bom:6.1.3"))
    testImplementation("org.junit.jupiter:junit-jupiter")
    testRuntimeOnly("org.junit.platform:junit-platform-launcher")
}

// The contract is the source of truth; generate from the repository's proto/.
sourceSets {
    main {
        proto {
            srcDir("../../proto")
        }
    }
}

protobuf {
    protoc {
        artifact = "com.google.protobuf:protoc:$protobufVersion"
    }
}

application {
    mainClass = "wrinkle.Main"
    applicationName = "wrinkle"
    applicationDefaultJvmArgs =
        listOf(
            "-Dorg.slf4j.simpleLogger.showDateTime=true",
            "-Dorg.slf4j.simpleLogger.log.org.apache.kafka=warn",
            "-Dorg.slf4j.simpleLogger.log.com.zaxxer.hikari=warn",
            "--enable-native-access=ALL-UNNAMED",
        )
}

tasks.withType<JavaCompile>().configureEach {
    options.encoding = "UTF-8"
    options.release = 25
    options.compilerArgs.addAll(listOf("-Xlint:all,-processing,-serial,-classfile", "-Werror"))
}

tasks.test {
    useJUnitPlatform()
}

spotless {
    java {
        target("src/**/*.java")
        googleJavaFormat()
    }
}
