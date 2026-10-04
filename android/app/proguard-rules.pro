# kotlinx.serialization: keep generated serializers of the protocol classes
-keepclassmembers @kotlinx.serialization.Serializable class net.nicodroid.zweep.** {
    *** Companion;
    kotlinx.serialization.KSerializer serializer(...);
}
-keepattributes *Annotation*, InnerClasses
