// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package net.nicodroid.zweep

import android.app.Application
import net.nicodroid.zweep.service.Engine

class ZweepApp : Application() {
    override fun onCreate() {
        super.onCreate()
        Engine.init(this)
    }
}
