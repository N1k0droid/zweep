package net.nicodroid.zweep.net

import java.security.cert.CertificateException
import java.security.cert.CertificateFactory
import java.security.cert.X509Certificate
import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Test

/** Pin checks with certificates generated once (resources/pins): company CA, self-signed, attacker */
class PinTest {
    private fun load(name: String): Array<X509Certificate> =
        CertificateFactory.getInstance("X.509").generateCertificates(javaClass.getResourceAsStream("/pins/$name"))
            .map { it as X509Certificate }.toTypedArray()

    private fun pin(c: X509Certificate) = java.util.Base64.getEncoder().encodeToString(
        java.security.MessageDigest.getInstance("SHA-256").digest(c.publicKey.encoded))

    @Test
    fun companyCaPinAcceptsItsServers() {
        val ca = load("ca.pem")[0]
        val tm = PinTrustManager(setOf(pin(ca)))
        tm.checkServerTrusted(load("chain.pem"), "ECDHE_ECDSA")
        assertEquals(1, pinnedIndex(load("chain.pem"), setOf(pin(ca))))
    }

    @Test
    fun companyCaAppendedToAnotherCertificateIsRefused() {
        // The attacker shows his own certificate with the real (public) CA certificate appended
        val ca = load("ca.pem")[0]
        assertThrows(CertificateException::class.java) { PinTrustManager(setOf(pin(ca))).checkServerTrusted(load("attack.pem"), "ECDHE_ECDSA") }
    }

    @Test
    fun selfSignedPinAndUnknownKeys() {
        val self = load("self.pem")
        PinTrustManager(setOf(pin(self[0]))).checkServerTrusted(self, "ECDHE_ECDSA")
        assertEquals(0, pinnedIndex(self, setOf(pin(self[0]))))
        assertThrows(CertificateException::class.java) { PinTrustManager(setOf(pin(self[0]))).checkServerTrusted(load("chain.pem"), "ECDHE_ECDSA") }
    }
}
