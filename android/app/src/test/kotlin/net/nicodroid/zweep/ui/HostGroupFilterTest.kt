// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package net.nicodroid.zweep.ui

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class HostGroupFilterTest {
    @Test
    fun subgroupsAreIncluded() {
        val mysql = listOf("Lab/Database/MySQL")
        assertTrue(inHostGroups(mysql, emptySet()))                  // no selection: all
        assertTrue(inHostGroups(mysql, setOf("Lab/Database")))       // parent covers the subgroup
        assertTrue(inHostGroups(mysql, setOf("Lab/Database/MySQL")))
        assertFalse(inHostGroups(mysql, setOf("Lab/Database/My")))   // a prefix of the name is not a parent
        assertFalse(inHostGroups(mysql, setOf("Lab/Web")))
        assertTrue(inHostGroups(listOf("Lab/Web", "Linux servers"), setOf("Linux servers", "Lab/Network")))
        assertFalse(inHostGroups(emptyList(), setOf("Lab")))
    }
}

class HostGroupTreeTest {
    @Test
    fun indentedOnlyUnderAPresentAncestor() {
        val rows = hostGroupTree(listOf("Lab/Network/Core", "Lab/Web", "Lab/Database/MySQL", "Lab/Database", "Linux servers"))
        val view = rows.map { "  ".repeat(it.depth) + it.label }
        org.junit.Assert.assertEquals(
            listOf("Lab/Database", "  MySQL", "Lab/Network/Core", "Lab/Web", "Linux servers"),
            view,
        )
    }
}
