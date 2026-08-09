//! Shared retro styling: the neon palette, text spawning helpers, and the
//! cursor→world helper every mouse game uses. Design space is a fixed
//! 720×640 window (world coords −360..360 × −320..320).

use bevy::prelude::*;

pub const SCREEN_W: f32 = 720.0;
pub const SCREEN_H: f32 = 640.0;

pub const GREEN: Color = Color::srgb(0.49, 0.99, 0.60);
pub const MAGENTA: Color = Color::srgb(1.0, 0.47, 1.0);
pub const AMBER: Color = Color::srgb(1.0, 0.82, 0.40);
pub const CYAN: Color = Color::srgb(0.45, 0.95, 1.0);
pub const RED: Color = Color::srgb(1.0, 0.33, 0.47);
pub const DIM: Color = Color::srgb(0.42, 0.42, 0.53);
pub const WHITE: Color = Color::srgb(0.92, 0.92, 0.95);

/// Twelve distinguishable player colors (Powder Keg, Hexfection).
pub const PLAYER_COLORS: [Color; 12] = [
    Color::srgb(0.49, 0.99, 0.60), // green
    Color::srgb(1.00, 0.47, 1.00), // magenta
    Color::srgb(1.00, 0.82, 0.40), // amber
    Color::srgb(0.45, 0.95, 1.00), // cyan
    Color::srgb(1.00, 0.33, 0.47), // red
    Color::srgb(0.62, 0.62, 1.00), // periwinkle
    Color::srgb(1.00, 0.65, 0.30), // orange
    Color::srgb(0.55, 1.00, 0.30), // lime
    Color::srgb(0.30, 0.60, 1.00), // blue
    Color::srgb(1.00, 1.00, 0.55), // yellow
    Color::srgb(0.95, 0.55, 0.75), // pink
    Color::srgb(0.60, 0.90, 0.85), // teal
];

/// Spawns a Text2d at a position; returns the entity for later mutation.
pub fn text(
    commands: &mut Commands,
    s: &str,
    size: f32,
    color: Color,
    pos: Vec3,
) -> Entity {
    commands
        .spawn((
            Text2d::new(s),
            TextFont { font_size: size, ..default() },
            TextColor(color),
            Transform::from_translation(pos),
        ))
        .id()
}

/// Current cursor position in world coordinates, if over the canvas.
pub fn cursor_world(
    window: &Window,
    camera: &Camera,
    cam_transform: &GlobalTransform,
) -> Option<Vec2> {
    let pos = window.cursor_position()?;
    camera.viewport_to_world_2d(cam_transform, pos).ok()
}
