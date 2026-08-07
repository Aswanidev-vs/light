# Add project specific ProGuard rules here.
# You can control the set of applied configuration files using the
# proguardFiles setting in build.gradle.

# Keep native methods
-keepclasseswithmembernames class * {
    native <methods>;
}

# Keep all classes and members in Wails application package to prevent JNI NoSuchMethodError crashes
-keep class com.wails.app.** { *; }
-keepclassmembers class com.wails.app.** { *; }

# Suppress warnings for missing compile-only annotations in third-party libraries (Tink, security-crypto)
-dontwarn javax.annotation.**
-dontwarn org.checkerframework.**
-dontwarn com.google.errorprone.annotations.**
-dontwarn com.google.crypto.tink.**
