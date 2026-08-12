//! The Top Secret arcade cartridge: seven cabinets in one Bevy binary.
//!
//! The page sets `window.__ARCADE_GAME` before init to choose the cabinet;
//! `arcade_insert_credit(players, humans)` starts a round; the cartridge
//! calls `window.__arcadeScore(n)` when the round ends. Engine: core bevy
//! only — no third-party bevy extension crates.

use bevy::prelude::*;

mod brickfall;
mod chess_ui;
mod comet;
mod go_ui;
mod hex_ui;
mod holdem;
mod interns;
mod night_audit;
mod penny;
mod powder;
mod red_tape;
mod retro;
mod rng;
mod shell;

/// Every cabinet shares one lifecycle. Games implement only `Playing`;
/// the shell owns Attract and GameOver.
#[derive(States, Clone, Copy, PartialEq, Eq, Hash, Debug, Default)]
pub enum Phase {
    #[default]
    Attract,
    Playing,
    GameOver,
}

/// What the coin slot asked for (multiplayer cabinets read both fields).
#[derive(Resource, Clone, Copy)]
pub struct CabinetConfig {
    pub players: u32,
    pub humans: u32,
}

/// The score the shell reports when a round ends.
#[derive(Resource, Default)]
pub struct FinalScore(pub u32);

/// Marker for everything a game spawns; swept by the shell between rounds.
#[derive(Component)]
pub struct GameTag;

/// Esc pause (local rounds only — a networked simulation waits for no one).
#[derive(Resource, Default)]
pub struct Paused(pub bool);

/// Run condition for the real-time games' update tuples.
pub fn unpaused(paused: Res<Paused>) -> bool {
    !paused.0
}

/// Networked-round configuration, provided by the page when a room starts.
/// `present[s]` is true when seat s is a live human somewhere on the network;
/// absent seats are bots, driven by the host (seat 0) and relayed like moves.
#[derive(Clone)]
pub struct NetCfg {
    pub seat: u8,
    pub seats: u8,
    pub present: Vec<bool>,
}

impl NetCfg {
    pub fn is_host(&self) -> bool {
        self.seat == 0
    }
}

/// None = local play (hotseat/bots on this machine).
#[derive(Resource, Default)]
pub struct NetMode(pub Option<NetCfg>);

/// One inbound network event: a relayed game message or a departure notice.
#[derive(Event)]
pub struct NetIn {
    pub seat: u8,
    pub left: bool,
    /// JSON payload of the game message (empty for departures).
    pub data: String,
}

pub fn run() {
    let game = shell::selected_game();
    let mut app = App::new();
    app.add_plugins(
        DefaultPlugins.set(WindowPlugin {
            primary_window: Some(Window {
                canvas: Some("#arcade-canvas".into()),
                resolution: (retro::SCREEN_W, retro::SCREEN_H).into(),
                resizable: false,
                ..default()
            }),
            ..default()
        }),
    )
    .insert_resource(ClearColor(Color::srgb(0.01, 0.01, 0.03)))
    .insert_resource(CabinetConfig { players: 1, humans: 1 })
    .init_resource::<FinalScore>()
    .init_resource::<NetMode>()
    .init_resource::<Paused>()
    .add_event::<NetIn>()
    .insert_resource(rng::Rng::seeded())
    .add_systems(Update, retro::popup_update.run_if(unpaused))
    .init_state::<Phase>();

    match game.as_str() {
        "chess" => {
            app.add_plugins((shell_for("CHESS", chess_ui::BLURB), chess_ui::ChessPlugin));
        }
        "go" => {
            app.add_plugins((shell_for("GO", go_ui::BLURB), go_ui::GoPlugin));
        }
        "comet-buster" => {
            app.add_plugins((shell_for("COMET BUSTER", comet::BLURB), comet::CometPlugin));
        }
        "penny-pincher" => {
            app.add_plugins((shell_for("PENNY PINCHER", penny::BLURB), penny::PennyPlugin));
        }
        "powder-keg" => {
            app.add_plugins((shell_for("POWDER KEG", powder::BLURB), powder::PowderPlugin));
        }
        "hexfection" => {
            app.add_plugins((shell_for("HEXFECTION", hex_ui::BLURB), hex_ui::HexPlugin));
        }
        "interns" => {
            app.add_plugins((shell_for("INTERNS", interns::BLURB), interns::InternsPlugin));
        }
        "texas-holdem" => {
            app.add_plugins((shell_for("HOLD 'EM", holdem::BLURB), holdem::HoldemPlugin));
        }
        "red-tape" => {
            app.add_plugins((shell_for("RED TAPE", red_tape::BLURB), red_tape::RedTapePlugin));
        }
        "night-audit" => {
            app.add_plugins((shell_for("NIGHT AUDIT", night_audit::BLURB), night_audit::NightAuditPlugin));
        }
        _ => {
            app.add_plugins((shell_for("BRICKFALL", brickfall::BLURB), brickfall::BrickPlugin));
        }
    }
    app.run();
}

fn shell_for(title: &'static str, blurb: &'static [&'static str]) -> shell::ShellPlugin {
    shell::ShellPlugin { title, blurb }
}

#[cfg(target_arch = "wasm32")]
mod wasm_api {
    use wasm_bindgen::prelude::*;

    /// Runs automatically when the page awaits `init()`.
    #[wasm_bindgen(start)]
    pub fn start() {
        crate::run();
    }

    /// The coin slot. Called by the page after the credit is logged server-side.
    #[wasm_bindgen]
    pub fn arcade_insert_credit(players: u32, humans: u32) {
        crate::shell::push_credit(players, humans);
    }

    /// Starts a networked round; cfg is JSON {seat, seats, present:[seat...]}.
    #[wasm_bindgen]
    pub fn arcade_start_net(cfg: String) {
        crate::shell::push_net_start(cfg);
    }

    /// Delivers one relayed room event; msg is JSON {seat, data} or {seat, left}.
    #[wasm_bindgen]
    pub fn arcade_net_event(msg: String) {
        crate::shell::push_net_event(msg);
    }

    /// Opens the active cabinet's level editor — a tool, so no credit
    /// required. INTERNS, Powder Keg, Hexfection, and Chess honor it.
    #[wasm_bindgen]
    pub fn arcade_start_editor() {
        crate::shell::request_editor();
    }
}
