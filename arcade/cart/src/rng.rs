//! A tiny xorshift* PRNG. The cartridge needs no cryptographic randomness,
//! and rolling our own keeps the dependency tree at exactly: bevy.

use bevy::prelude::Resource;

#[derive(Resource)]
pub struct Rng(u64);

impl Rng {
    pub fn seeded() -> Rng {
        #[cfg(target_arch = "wasm32")]
        let seed = js_sys::Date::now() as u64 | 1;
        #[cfg(not(target_arch = "wasm32"))]
        let seed = 0x9E37_79B9_7F4A_7C15;
        Rng(seed)
    }

    pub fn next_u64(&mut self) -> u64 {
        let mut x = self.0;
        x ^= x >> 12;
        x ^= x << 25;
        x ^= x >> 27;
        self.0 = x;
        x.wrapping_mul(0x2545_F491_4F6C_DD1D)
    }

    /// Uniform integer in `0..n` (n must be > 0).
    pub fn range(&mut self, n: u32) -> u32 {
        (self.next_u64() % n as u64) as u32
    }

    /// Uniform float in `0.0..1.0`.
    pub fn unit(&mut self) -> f32 {
        (self.next_u64() >> 40) as f32 / (1u64 << 24) as f32
    }

    /// True with probability `p`.
    pub fn chance(&mut self, p: f32) -> bool {
        self.unit() < p
    }

    /// Uniform float in `lo..hi`.
    pub fn between(&mut self, lo: f32, hi: f32) -> f32 {
        lo + self.unit() * (hi - lo)
    }
}
