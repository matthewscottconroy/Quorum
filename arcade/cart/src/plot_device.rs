//! PLOT DEVICE — a full turtle-graphics language lab in a cabinet. Type
//! programs at the console; turtles draw them. The dialect is a classic
//! screen-turtle Logo (public-domain territory: Papert's turtle geometry),
//! implemented from scratch:
//!
//!   - Motion: FD BK RT LT SETXY SETX SETY SETH HOME ARC
//!   - Pen:    PU PD SETPC 0-15, SETRGB r g b, SETW n, SETSTYLE "SOLID/"DASH/"DOT
//!   - Screen: CS HT ST SETBG, TAB toggles the console for a clean look
//!   - Turtles: NEWTURTLE (outputs an id), TELL id, ASK id [block], EACH [block]
//!   - Control: REPEAT/REPCOUNT, IF, IFELSE, WHILE, FOR, STOP, OUTPUT
//!   - Words:  MAKE "x, :x, arithmetic, RANDOM, SIN COS SQRT ABS ROUND,
//!             XCOR YCOR HEADING, PRINT
//!   - Lists:  [ ... ] FIRST BUTFIRST LAST ITEM COUNT FPUT LPUT LIST SE
//!   - Functions are values: FN [params] [body] makes one, APPLY calls one,
//!     quoted proc names work too, and MAP / FILTER / REDUCE / FOREACH / RUN
//!     take them as arguments. TO name :a :b ... END defines procedures at
//!     the console (multi-line, END finishes) — SAVE "name shelves the whole
//!     workspace as a level, and the LEVEL picker loads it back.
//!
//! Programs get an instruction budget per line (no cabinet freezes) and a
//! finite ink supply (segment cap). QUIT ends the session for the ladder.

use std::collections::HashMap;
use std::sync::Arc as Rc;

use bevy::input::keyboard::{Key, KeyboardInput};
use bevy::prelude::*;
use bevy::sprite::Anchor;

use crate::retro::{AMBER, CYAN, DIM, GREEN, WHITE};
use crate::rng::Rng;
use crate::shell::{save_level, sfx, stat};
use crate::{FinalScore, GameTag, Phase};

pub const BLURB: &[&str] = &[
    "A TURTLE, A PEN, AND A WHOLE LANGUAGE. TYPE PROGRAMS, WATCH THEM DRAW.",
    "TRY: REPEAT 12 [SETPC REPCOUNT REPEAT 4 [FD 90 RT 90] RT 30]",
    "TO NAME :ARG ... END DEFINES PROCEDURES. HELP LISTS EVERYTHING.",
];

const PALETTE: [Color; 16] = [
    Color::srgb(0.08, 0.08, 0.10), // 0 ink black
    Color::srgb(0.20, 0.35, 0.95), // 1 blue
    Color::srgb(0.20, 0.75, 0.30), // 2 green
    Color::srgb(0.20, 0.80, 0.85), // 3 cyan
    Color::srgb(0.92, 0.25, 0.20), // 4 red
    Color::srgb(0.90, 0.30, 0.85), // 5 magenta
    Color::srgb(0.95, 0.85, 0.25), // 6 yellow
    Color::srgb(0.95, 0.95, 0.92), // 7 white
    Color::srgb(0.55, 0.38, 0.20), // 8 brown
    Color::srgb(0.85, 0.72, 0.50), // 9 tan
    Color::srgb(0.10, 0.45, 0.22), // 10 forest
    Color::srgb(0.35, 0.80, 0.70), // 11 aqua
    Color::srgb(0.95, 0.60, 0.55), // 12 salmon
    Color::srgb(0.55, 0.30, 0.85), // 13 purple
    Color::srgb(0.95, 0.55, 0.15), // 14 orange
    Color::srgb(0.60, 0.62, 0.68), // 15 gray
];

const SEG_CAP: usize = 30_000;
const OP_BUDGET: u32 = 400_000;
const DEPTH_CAP: usize = 150;

const DEMO_SRC: &str = r#"
TO SQUARE :S REPEAT 4 [FD :S RT 90] END
TO POLY :N :S REPEAT :N [FD :S RT 360 / :N] END
TO STAR :S REPEAT 5 [FD :S RT 144] END
TO SPIRO :S :A REPEAT 150 [FD :S + REPCOUNT * 1.4 RT :A SETPC 1 + REPCOUNT % 15] END
TO TREE :S IF :S < 7 [STOP] SETW :S / 18 FD :S LT 24 TREE :S * 0.72 RT 49 TREE :S * 0.72 LT 25 BK :S END
TO BURST :N REPEAT :N [SETPC 1 + REPCOUNT % 15 FD 150 BK 150 RT 360 / :N] END
TO DASHY REPEAT 8 [SETSTYLE "DASH FD 60 SETSTYLE "DOT FD 60 RT 45] END
"#;

// ── tokens ───────────────────────────────────────────────────────────────

#[derive(Clone, Debug, PartialEq)]
enum Tok {
    Num(f64),
    Word(String),  // bare word: command / procedure name
    QWord(String), // "quoted
    Var(String),   // :name
    LB,
    RB,
    LP,
    RP,
    Op(&'static str),
}

fn lex(src: &str) -> Result<Vec<Tok>, String> {
    let mut out = Vec::new();
    let chars: Vec<char> = src.chars().collect();
    let mut i = 0;
    while i < chars.len() {
        let c = chars[i];
        match c {
            ';' => {
                while i < chars.len() && chars[i] != '\n' {
                    i += 1;
                }
            }
            c if c.is_whitespace() => i += 1,
            '[' => {
                out.push(Tok::LB);
                i += 1;
            }
            ']' => {
                out.push(Tok::RB);
                i += 1;
            }
            '(' => {
                out.push(Tok::LP);
                i += 1;
            }
            ')' => {
                out.push(Tok::RP);
                i += 1;
            }
            '+' => {
                out.push(Tok::Op("+"));
                i += 1;
            }
            '-' => {
                out.push(Tok::Op("-"));
                i += 1;
            }
            '*' => {
                out.push(Tok::Op("*"));
                i += 1;
            }
            '/' => {
                out.push(Tok::Op("/"));
                i += 1;
            }
            '%' => {
                out.push(Tok::Op("%"));
                i += 1;
            }
            '=' => {
                out.push(Tok::Op("="));
                i += 1;
            }
            '<' => {
                if chars.get(i + 1) == Some(&'=') {
                    out.push(Tok::Op("<="));
                    i += 2;
                } else if chars.get(i + 1) == Some(&'>') {
                    out.push(Tok::Op("<>"));
                    i += 2;
                } else {
                    out.push(Tok::Op("<"));
                    i += 1;
                }
            }
            '>' => {
                if chars.get(i + 1) == Some(&'=') {
                    out.push(Tok::Op(">="));
                    i += 2;
                } else {
                    out.push(Tok::Op(">"));
                    i += 1;
                }
            }
            '"' | ':' => {
                let quote = c == '"';
                i += 1;
                let start = i;
                while i < chars.len() && (chars[i].is_alphanumeric() || chars[i] == '.' || chars[i] == '_' || chars[i] == '?') {
                    i += 1;
                }
                let w: String = chars[start..i].iter().collect::<String>().to_uppercase();
                if w.is_empty() {
                    return Err(format!("LONELY {c}"));
                }
                out.push(if quote { Tok::QWord(w) } else { Tok::Var(w) });
            }
            c if c.is_ascii_digit() || c == '.' => {
                let start = i;
                while i < chars.len() && (chars[i].is_ascii_digit() || chars[i] == '.') {
                    i += 1;
                }
                let s: String = chars[start..i].iter().collect();
                out.push(Tok::Num(s.parse().map_err(|_| format!("BAD NUMBER {s}"))?));
            }
            c if c.is_alphanumeric() || c == '_' || c == '?' || c == '.' => {
                let start = i;
                while i < chars.len() && (chars[i].is_alphanumeric() || chars[i] == '.' || chars[i] == '_' || chars[i] == '?') {
                    i += 1;
                }
                let w: String = chars[start..i].iter().collect::<String>().to_uppercase();
                out.push(Tok::Word(w));
            }
            other => return Err(format!("I CAN'T READ '{other}'")),
        }
    }
    Ok(out)
}

// ── values ───────────────────────────────────────────────────────────────

#[derive(Clone)]
enum Val {
    Num(f64),
    Word(String),
    List(Rc<Vec<Val>>),
    Func(Rc<ProcDef>),
    Nothing,
}

struct ProcDef {
    name: String, // "" for lambdas
    params: Vec<String>,
    body: Vec<Tok>,
    src: String, // pretty source for PO / SAVE ("" for lambdas)
}

fn fmt_num(v: f64) -> String {
    if (v - v.round()).abs() < 1e-9 {
        format!("{}", v.round() as i64)
    } else {
        let s = format!("{v:.4}");
        s.trim_end_matches('0').trim_end_matches('.').to_string()
    }
}

fn fmt_val(v: &Val) -> String {
    match v {
        Val::Num(n) => fmt_num(*n),
        Val::Word(w) => w.clone(),
        Val::List(items) => {
            let inner: Vec<String> = items.iter().map(fmt_val).collect();
            format!("[{}]", inner.join(" "))
        }
        Val::Func(p) => {
            if p.name.is_empty() {
                "(FN)".into()
            } else {
                format!("(FN {})", p.name)
            }
        }
        Val::Nothing => "(NOTHING)".into(),
    }
}

fn truthy(v: &Val) -> bool {
    match v {
        Val::Num(n) => *n != 0.0,
        Val::Word(w) => w == "TRUE",
        Val::List(l) => !l.is_empty(),
        _ => false,
    }
}

/// Bracketed groups become list values; RUN turns them back into tokens.
fn toks_to_list(cur: &mut Cursor) -> Result<Val, String> {
    let mut items = Vec::new();
    loop {
        match cur.next().cloned() {
            None => return Err("MISSING ]".into()),
            Some(Tok::RB) => return Ok(Val::List(Rc::new(items))),
            Some(Tok::LB) => items.push(toks_to_list(cur)?),
            Some(Tok::Num(n)) => items.push(Val::Num(n)),
            Some(Tok::Word(w)) => items.push(Val::Word(w)),
            Some(Tok::QWord(w)) => items.push(Val::Word(format!("\"{w}"))),
            Some(Tok::Var(w)) => items.push(Val::Word(format!(":{w}"))),
            Some(Tok::Op(o)) => items.push(Val::Word(o.to_string())),
            Some(Tok::LP) => items.push(Val::Word("(".into())),
            Some(Tok::RP) => items.push(Val::Word(")".into())),
        }
    }
}

fn list_to_toks(items: &[Val]) -> Result<Vec<Tok>, String> {
    let mut out = Vec::new();
    for it in items {
        match it {
            Val::Num(n) => out.push(Tok::Num(*n)),
            Val::List(inner) => {
                out.push(Tok::LB);
                out.extend(list_to_toks(inner)?);
                out.push(Tok::RB);
            }
            Val::Word(w) => out.extend(lex(w)?),
            Val::Func(_) => return Err("A FUNCTION VALUE CAN'T RUN AS TEXT".into()),
            Val::Nothing => {}
        }
    }
    Ok(out)
}

struct Cursor<'a> {
    toks: &'a [Tok],
    i: usize,
}

impl<'a> Cursor<'a> {
    fn peek(&self) -> Option<&Tok> {
        self.toks.get(self.i)
    }
    fn next(&mut self) -> Option<&Tok> {
        let t = self.toks.get(self.i);
        if t.is_some() {
            self.i += 1;
        }
        t
    }
}

// ── the drawing board ────────────────────────────────────────────────────

struct Seg {
    a: Vec2,
    b: Vec2,
    color: Color,
    width: f32,
    style: u8, // 0 solid, 1 dash, 2 dot
}

struct TurtleSt {
    x: f32,
    y: f32,
    heading: f32, // degrees clockwise from north
    pen: bool,
    color: Color,
    width: f32,
    style: u8,
    visible: bool,
}

impl TurtleSt {
    fn hatch() -> Self {
        TurtleSt { x: 0.0, y: 0.0, heading: 0.0, pen: true, color: PALETTE[7], width: 2.0, style: 0, visible: true }
    }
}

struct BoardSt {
    turtles: Vec<TurtleSt>,
    active: usize,
    segs: Vec<Seg>,
    spawned: usize, // segs already turned into sprites
    clear_req: bool,
    bg: usize,
    ink_dry_said: bool,
    strokes_total: u64,
}

impl BoardSt {
    fn cur(&mut self) -> &mut TurtleSt {
        let i = self.active.min(self.turtles.len().saturating_sub(1));
        &mut self.turtles[i]
    }
    fn move_by(&mut self, dist: f32) {
        let i = self.active.min(self.turtles.len() - 1);
        let (nx, ny) = {
            let t = &self.turtles[i];
            let rad = t.heading.to_radians();
            (t.x + rad.sin() * dist, t.y + rad.cos() * dist)
        };
        self.line_to(nx, ny);
    }
    fn line_to(&mut self, nx: f32, ny: f32) {
        let i = self.active.min(self.turtles.len() - 1);
        let t = &self.turtles[i];
        if t.pen && self.segs.len() < SEG_CAP {
            self.segs.push(Seg {
                a: Vec2::new(t.x, t.y),
                b: Vec2::new(nx, ny),
                color: t.color,
                width: t.width,
                style: t.style,
            });
            self.strokes_total += 1;
        }
        let t = &mut self.turtles[i];
        t.x = nx;
        t.y = ny;
    }
}

// ── the interpreter ──────────────────────────────────────────────────────

enum Esc {
    Err(String),
    Stop,
    Out(Val),
}

impl From<String> for Esc {
    fn from(s: String) -> Self {
        Esc::Err(s)
    }
}

type R<T> = Result<T, Esc>;

struct Interp {
    procs: HashMap<String, Rc<ProcDef>>,
    globals: HashMap<String, Val>,
    frames: Vec<HashMap<String, Val>>,
    repcounts: Vec<f64>,
    ops: u32,
    prints: Vec<String>,
}

struct Ctx<'a> {
    board: &'a mut BoardSt,
    rng: &'a mut Rng,
}

impl Interp {
    fn new() -> Self {
        Interp {
            procs: HashMap::new(),
            globals: HashMap::new(),
            frames: Vec::new(),
            repcounts: Vec::new(),
            ops: 0,
            prints: Vec::new(),
        }
    }

    fn lookup(&self, name: &str) -> Option<Val> {
        for f in self.frames.iter().rev() {
            if let Some(v) = f.get(name) {
                return Some(v.clone());
            }
        }
        self.globals.get(name).cloned()
    }

    fn set_var(&mut self, name: &str, v: Val) {
        for f in self.frames.iter_mut().rev() {
            if f.contains_key(name) {
                f.insert(name.into(), v);
                return;
            }
        }
        self.globals.insert(name.into(), v);
    }

    /// Runs a token sequence as statements.
    fn run_toks(&mut self, ctx: &mut Ctx, toks: &[Tok]) -> R<()> {
        let mut cur = Cursor { toks, i: 0 };
        while cur.peek().is_some() {
            self.expr(ctx, &mut cur, 0)?; // command results are discarded
        }
        Ok(())
    }

    fn run_list_block(&mut self, ctx: &mut Ctx, v: &Val) -> R<Option<Val>> {
        let Val::List(items) = v else {
            return Err(Esc::Err(format!("EXPECTED A [BLOCK], GOT {}", fmt_val(v))));
        };
        let toks = list_to_toks(items).map_err(Esc::Err)?;
        // A block used as an expression may leave one value (WHILE conds).
        let mut cur = Cursor { toks: &toks, i: 0 };
        let mut last = Val::Nothing;
        while cur.peek().is_some() {
            last = self.expr(ctx, &mut cur, 0)?;
        }
        Ok(if matches!(last, Val::Nothing) { None } else { Some(last) })
    }

    fn want_num(&mut self, ctx: &mut Ctx, cur: &mut Cursor) -> R<f64> {
        match self.expr(ctx, cur, 1)? {
            Val::Num(n) => Ok(n),
            other => Err(Esc::Err(format!("EXPECTED A NUMBER, GOT {}", fmt_val(&other)))),
        }
    }

    fn want_val(&mut self, ctx: &mut Ctx, cur: &mut Cursor) -> R<Val> {
        match self.expr(ctx, cur, 1)? {
            Val::Nothing => Err(Esc::Err("THAT DIDN'T OUTPUT A VALUE".into())),
            v => Ok(v),
        }
    }

    fn want_list(&mut self, ctx: &mut Ctx, cur: &mut Cursor) -> R<Rc<Vec<Val>>> {
        match self.want_val(ctx, cur)? {
            Val::List(l) => Ok(l),
            other => Err(Esc::Err(format!("EXPECTED A [LIST], GOT {}", fmt_val(&other)))),
        }
    }

    fn want_fn(&mut self, ctx: &mut Ctx, cur: &mut Cursor) -> R<Rc<ProcDef>> {
        let v = self.want_val(ctx, cur)?;
        self.as_fn(&v)
    }

    fn as_fn(&self, v: &Val) -> R<Rc<ProcDef>> {
        match v {
            Val::Func(f) => Ok(f.clone()),
            Val::Word(w) => {
                let name = w.trim_start_matches('"');
                self.procs
                    .get(name)
                    .cloned()
                    .ok_or_else(|| Esc::Err(format!("I DON'T KNOW A PROCEDURE CALLED {name}")))
            }
            other => Err(Esc::Err(format!("{} ISN'T A FUNCTION", fmt_val(other)))),
        }
    }

    fn apply(&mut self, ctx: &mut Ctx, f: &Rc<ProcDef>, args: Vec<Val>) -> R<Val> {
        if args.len() != f.params.len() {
            return Err(Esc::Err(format!(
                "{} WANTS {} INPUT(S), GOT {}",
                if f.name.is_empty() { "THAT FN" } else { &f.name },
                f.params.len(),
                args.len()
            )));
        }
        if self.frames.len() > DEPTH_CAP {
            return Err(Esc::Err("TOO DEEP (RECURSION LIMIT)".into()));
        }
        let mut frame = HashMap::new();
        for (p, a) in f.params.iter().zip(args) {
            frame.insert(p.clone(), a);
        }
        self.frames.push(frame);
        let body = f.body.clone();
        // The last expression's value is the implicit OUTPUT — this is what
        // makes FN [x] [:x * :x] pleasant inside MAP and friends. Explicit
        // OUTPUT and STOP still win.
        let result = (|| -> R<Val> {
            let mut cur = Cursor { toks: &body, i: 0 };
            let mut last = Val::Nothing;
            while cur.peek().is_some() {
                last = match self.expr(ctx, &mut cur, 0) {
                    Ok(v) => v,
                    Err(Esc::Stop) => return Ok(Val::Nothing),
                    Err(Esc::Out(v)) => return Ok(v),
                    Err(e) => return Err(e),
                };
            }
            Ok(last)
        })();
        self.frames.pop();
        result
    }

    /// Pratt-style expression: primaries are prefix calls with known arity.
    fn expr(&mut self, ctx: &mut Ctx, cur: &mut Cursor, min_prec: u8) -> R<Val> {
        let mut lhs = self.unary(ctx, cur)?;
        loop {
            let (op, prec): (&str, u8) = match cur.peek() {
                Some(Tok::Op(o)) => {
                    let p = match *o {
                        "*" | "/" | "%" => 3,
                        "+" | "-" => 2,
                        _ => 1,
                    };
                    (*o, p)
                }
                _ => break,
            };
            if prec < min_prec.max(1) {
                break;
            }
            cur.next();
            let rhs = self.expr(ctx, cur, prec + 1)?;
            let (a, b) = match (&lhs, &rhs) {
                (Val::Num(a), Val::Num(b)) => (*a, *b),
                _ => {
                    // Equality works on words/lists too.
                    if op == "=" || op == "<>" {
                        let eq = fmt_val(&lhs) == fmt_val(&rhs);
                        lhs = Val::Num(if eq == (op == "=") { 1.0 } else { 0.0 });
                        continue;
                    }
                    return Err(Esc::Err(format!("CAN'T {op} {} AND {}", fmt_val(&lhs), fmt_val(&rhs))));
                }
            };
            lhs = Val::Num(match op {
                "+" => a + b,
                "-" => a - b,
                "*" => a * b,
                "/" => {
                    if b == 0.0 {
                        return Err(Esc::Err("DIVIDED BY ZERO".into()));
                    }
                    a / b
                }
                "%" => {
                    if b == 0.0 {
                        return Err(Esc::Err("MOD BY ZERO".into()));
                    }
                    a.rem_euclid(b)
                }
                "<" => (a < b) as i32 as f64,
                ">" => (a > b) as i32 as f64,
                "<=" => (a <= b) as i32 as f64,
                ">=" => (a >= b) as i32 as f64,
                "=" => (a == b) as i32 as f64,
                "<>" => (a != b) as i32 as f64,
                _ => unreachable!(),
            });
        }
        Ok(lhs)
    }

    fn unary(&mut self, ctx: &mut Ctx, cur: &mut Cursor) -> R<Val> {
        if let Some(Tok::Op("-")) = cur.peek() {
            cur.next();
            let v = self.unary(ctx, cur)?;
            return match v {
                Val::Num(n) => Ok(Val::Num(-n)),
                other => Err(Esc::Err(format!("CAN'T NEGATE {}", fmt_val(&other)))),
            };
        }
        self.primary(ctx, cur)
    }

    fn primary(&mut self, ctx: &mut Ctx, cur: &mut Cursor) -> R<Val> {
        self.ops += 1;
        if self.ops > OP_BUDGET {
            return Err(Esc::Err("THAT'S TOO MUCH WORK FOR ONE LINE (BUDGET)".into()));
        }
        let tok = cur.next().cloned().ok_or_else(|| Esc::Err("RAN OUT OF INPUTS".into()))?;
        match tok {
            Tok::Num(n) => Ok(Val::Num(n)),
            Tok::QWord(w) => Ok(Val::Word(format!("\"{w}"))),
            Tok::Var(name) => self
                .lookup(&name)
                .ok_or_else(|| Esc::Err(format!(":{name} HAS NO VALUE"))),
            Tok::LB => toks_to_list(cur).map_err(Esc::Err),
            Tok::LP => {
                let v = self.expr(ctx, cur, 1)?;
                match cur.next() {
                    Some(Tok::RP) => Ok(v),
                    _ => Err(Esc::Err("MISSING )".into())),
                }
            }
            Tok::RB => Err(Esc::Err("UNEXPECTED ]".into())),
            Tok::RP => Err(Esc::Err("UNEXPECTED )".into())),
            Tok::Op(o) => Err(Esc::Err(format!("UNEXPECTED {o}"))),
            Tok::Word(name) => self.call(ctx, cur, &name),
        }
    }

    #[allow(clippy::too_many_lines)]
    fn call(&mut self, ctx: &mut Ctx, cur: &mut Cursor, name: &str) -> R<Val> {
        macro_rules! num {
            () => {
                self.want_num(ctx, cur)?
            };
        }
        match name {
            // motion
            "FD" | "FORWARD" => {
                let d = num!();
                ctx.board.move_by(d as f32);
                Ok(Val::Nothing)
            }
            "BK" | "BACK" => {
                let d = num!();
                ctx.board.move_by(-d as f32);
                Ok(Val::Nothing)
            }
            "RT" | "RIGHT" => {
                let d = num!();
                ctx.board.cur().heading += d as f32;
                Ok(Val::Nothing)
            }
            "LT" | "LEFT" => {
                let d = num!();
                ctx.board.cur().heading -= d as f32;
                Ok(Val::Nothing)
            }
            "SETH" | "SETHEADING" => {
                let d = num!();
                ctx.board.cur().heading = d as f32;
                Ok(Val::Nothing)
            }
            "SETXY" => {
                let x = num!();
                let y = num!();
                ctx.board.line_to(x as f32, y as f32);
                Ok(Val::Nothing)
            }
            "SETX" => {
                let x = num!();
                let y = ctx.board.cur().y;
                ctx.board.line_to(x as f32, y);
                Ok(Val::Nothing)
            }
            "SETY" => {
                let y = num!();
                let x = ctx.board.cur().x;
                ctx.board.line_to(x, y as f32);
                Ok(Val::Nothing)
            }
            "HOME" => {
                ctx.board.line_to(0.0, 0.0);
                ctx.board.cur().heading = 0.0;
                Ok(Val::Nothing)
            }
            "ARC" => {
                // UCB style: an arc of :angle degrees at :radius, centered
                // on the turtle, starting at its heading. The turtle stays.
                let ang = num!();
                let r = num!() as f32;
                let (cx, cy, h0) = {
                    let t = ctx.board.cur();
                    (t.x, t.y, t.heading)
                };
                let steps = ((ang.abs() / 6.0).ceil() as usize).clamp(1, 360);
                let mut prev: Option<Vec2> = None;
                for k in 0..=steps {
                    let a = (h0 as f64 + ang * k as f64 / steps as f64).to_radians();
                    let p = Vec2::new(cx + (a.sin() as f32) * r, cy + (a.cos() as f32) * r);
                    if let Some(q) = prev {
                        if ctx.board.segs.len() < SEG_CAP {
                            let t = ctx.board.cur();
                            let (color, width, style, pen) = (t.color, t.width, t.style, t.pen);
                            if pen {
                                ctx.board.segs.push(Seg { a: q, b: p, color, width, style });
                                ctx.board.strokes_total += 1;
                            }
                        }
                    }
                    prev = Some(p);
                }
                Ok(Val::Nothing)
            }
            // pen
            "PU" | "PENUP" => {
                ctx.board.cur().pen = false;
                Ok(Val::Nothing)
            }
            "PD" | "PENDOWN" => {
                ctx.board.cur().pen = true;
                Ok(Val::Nothing)
            }
            "SETPC" | "SETCOLOR" => {
                let n = num!() as i64;
                ctx.board.cur().color = PALETTE[(n.rem_euclid(16)) as usize];
                Ok(Val::Nothing)
            }
            "SETRGB" => {
                let r = num!();
                let g = num!();
                let b = num!();
                ctx.board.cur().color =
                    Color::srgb((r / 255.0) as f32, (g / 255.0) as f32, (b / 255.0) as f32);
                Ok(Val::Nothing)
            }
            "SETW" | "SETWIDTH" => {
                let w = num!();
                ctx.board.cur().width = (w as f32).clamp(0.5, 24.0);
                Ok(Val::Nothing)
            }
            "SETSTYLE" => {
                let v = self.want_val(ctx, cur)?;
                let w = fmt_val(&v);
                ctx.board.cur().style = match w.trim_start_matches('"') {
                    "SOLID" => 0,
                    "DASH" => 1,
                    "DOT" => 2,
                    other => return Err(Esc::Err(format!("STYLE {other}? TRY \"SOLID \"DASH \"DOT"))),
                };
                Ok(Val::Nothing)
            }
            // screen & turtles
            "CS" | "CLEARSCREEN" => {
                // Clear NOW, so drawing later on the same line survives;
                // the sprite sweep happens at the next render pass.
                ctx.board.segs.clear();
                ctx.board.spawned = 0;
                ctx.board.ink_dry_said = false;
                ctx.board.clear_req = true;
                Ok(Val::Nothing)
            }
            "HT" | "HIDETURTLE" => {
                ctx.board.cur().visible = false;
                Ok(Val::Nothing)
            }
            "ST" | "SHOWTURTLE" => {
                ctx.board.cur().visible = true;
                Ok(Val::Nothing)
            }
            "SETBG" => {
                let n = num!() as i64;
                ctx.board.bg = (n.rem_euclid(16)) as usize;
                Ok(Val::Nothing)
            }
            "NEWTURTLE" => {
                if ctx.board.turtles.len() >= 24 {
                    return Err(Esc::Err("24 TURTLES IS PLENTY".into()));
                }
                ctx.board.turtles.push(TurtleSt::hatch());
                stat("turtles_hatched", 1);
                Ok(Val::Num((ctx.board.turtles.len() - 1) as f64))
            }
            "TELL" => {
                let n = num!() as usize;
                if n >= ctx.board.turtles.len() {
                    return Err(Esc::Err(format!("NO TURTLE {n}")));
                }
                ctx.board.active = n;
                Ok(Val::Nothing)
            }
            "ASK" => {
                let n = num!() as usize;
                let block = self.want_val(ctx, cur)?;
                if n >= ctx.board.turtles.len() {
                    return Err(Esc::Err(format!("NO TURTLE {n}")));
                }
                let prev = ctx.board.active;
                ctx.board.active = n;
                let r = self.run_list_block(ctx, &block);
                ctx.board.active = prev;
                r?;
                Ok(Val::Nothing)
            }
            "EACH" => {
                let block = self.want_val(ctx, cur)?;
                let prev = ctx.board.active;
                for n in 0..ctx.board.turtles.len() {
                    ctx.board.active = n;
                    if let Err(e) = self.run_list_block(ctx, &block) {
                        ctx.board.active = prev;
                        return Err(e);
                    }
                }
                ctx.board.active = prev;
                Ok(Val::Nothing)
            }
            "WHO" => Ok(Val::Num(ctx.board.active as f64)),
            "TURTLES" => Ok(Val::Num(ctx.board.turtles.len() as f64)),
            // queries
            "XCOR" => Ok(Val::Num(ctx.board.cur().x as f64)),
            "YCOR" => Ok(Val::Num(ctx.board.cur().y as f64)),
            "HEADING" => Ok(Val::Num(ctx.board.cur().heading as f64)),
            // control
            "REPEAT" => {
                let n = num!() as i64;
                let block = self.want_val(ctx, cur)?;
                for k in 1..=n.max(0) {
                    self.repcounts.push(k as f64);
                    let r = self.run_list_block(ctx, &block);
                    self.repcounts.pop();
                    r?;
                }
                Ok(Val::Nothing)
            }
            "REPCOUNT" => Ok(Val::Num(*self.repcounts.last().unwrap_or(&0.0))),
            "IF" => {
                let cond = self.want_val(ctx, cur)?;
                let block = self.want_val(ctx, cur)?;
                if truthy(&cond) {
                    self.run_list_block(ctx, &block)?;
                }
                Ok(Val::Nothing)
            }
            "IFELSE" => {
                let cond = self.want_val(ctx, cur)?;
                let yes = self.want_val(ctx, cur)?;
                let no = self.want_val(ctx, cur)?;
                let picked = if truthy(&cond) { yes } else { no };
                Ok(self.run_list_block(ctx, &picked)?.unwrap_or(Val::Nothing))
            }
            "WHILE" => {
                let cond = self.want_val(ctx, cur)?;
                let block = self.want_val(ctx, cur)?;
                loop {
                    let c = self
                        .run_list_block(ctx, &cond)?
                        .ok_or_else(|| Esc::Err("WHILE'S CONDITION MUST OUTPUT".into()))?;
                    if !truthy(&c) {
                        break;
                    }
                    self.run_list_block(ctx, &block)?;
                }
                Ok(Val::Nothing)
            }
            "FOR" => {
                // FOR [I 1 10 2] [ ... ]
                let spec = self.want_list(ctx, cur)?;
                let block = self.want_val(ctx, cur)?;
                if spec.len() < 3 {
                    return Err(Esc::Err("FOR WANTS [VAR START END STEP?]".into()));
                }
                let var = fmt_val(&spec[0]).trim_start_matches('"').to_string();
                let (start, end) = match (&spec[1], &spec[2]) {
                    (Val::Num(a), Val::Num(b)) => (*a, *b),
                    _ => return Err(Esc::Err("FOR'S BOUNDS MUST BE NUMBERS".into())),
                };
                let step = match spec.get(3) {
                    Some(Val::Num(s)) => *s,
                    _ => {
                        if end >= start {
                            1.0
                        } else {
                            -1.0
                        }
                    }
                };
                if step == 0.0 {
                    return Err(Esc::Err("FOR'S STEP CAN'T BE 0".into()));
                }
                let mut i = start;
                while (step > 0.0 && i <= end + 1e-9) || (step < 0.0 && i >= end - 1e-9) {
                    self.frames.push(HashMap::from([(var.clone(), Val::Num(i))]));
                    let r = self.run_list_block(ctx, &block);
                    self.frames.pop();
                    r?;
                    i += step;
                }
                Ok(Val::Nothing)
            }
            "STOP" => Err(Esc::Stop),
            "OUTPUT" | "OP" => {
                let v = self.want_val(ctx, cur)?;
                Err(Esc::Out(v))
            }
            // words & math
            "MAKE" => {
                let namev = self.want_val(ctx, cur)?;
                let v = self.want_val(ctx, cur)?;
                let n = fmt_val(&namev).trim_start_matches('"').to_string();
                self.set_var(&n, v);
                Ok(Val::Nothing)
            }
            "PRINT" | "PR" | "SHOW" => {
                let v = self.want_val(ctx, cur)?;
                self.prints.push(fmt_val(&v));
                Ok(Val::Nothing)
            }
            "RANDOM" => {
                let n = num!() as i64;
                if n <= 0 {
                    return Err(Esc::Err("RANDOM WANTS A POSITIVE NUMBER".into()));
                }
                Ok(Val::Num(ctx.rng.range(n as u32) as f64))
            }
            "SIN" => Ok(Val::Num(num!().to_radians().sin())),
            "COS" => Ok(Val::Num(num!().to_radians().cos())),
            "SQRT" => {
                let n = num!();
                if n < 0.0 {
                    return Err(Esc::Err("SQRT OF A NEGATIVE".into()));
                }
                Ok(Val::Num(n.sqrt()))
            }
            "ABS" => Ok(Val::Num(num!().abs())),
            "ROUND" => Ok(Val::Num(num!().round())),
            "INT" => Ok(Val::Num(num!().trunc())),
            "AND" => {
                let a = self.want_val(ctx, cur)?;
                let b = self.want_val(ctx, cur)?;
                Ok(Val::Num((truthy(&a) && truthy(&b)) as i32 as f64))
            }
            "OR" => {
                let a = self.want_val(ctx, cur)?;
                let b = self.want_val(ctx, cur)?;
                Ok(Val::Num((truthy(&a) || truthy(&b)) as i32 as f64))
            }
            "NOT" => {
                let a = self.want_val(ctx, cur)?;
                Ok(Val::Num(!truthy(&a) as i32 as f64))
            }
            // lists
            "LIST" => {
                let a = self.want_val(ctx, cur)?;
                let b = self.want_val(ctx, cur)?;
                Ok(Val::List(Rc::new(vec![a, b])))
            }
            "SE" | "SENTENCE" => {
                let a = self.want_val(ctx, cur)?;
                let b = self.want_val(ctx, cur)?;
                let mut out = Vec::new();
                for v in [a, b] {
                    match v {
                        Val::List(l) => out.extend(l.iter().cloned()),
                        other => out.push(other),
                    }
                }
                Ok(Val::List(Rc::new(out)))
            }
            "FIRST" => {
                let l = self.want_list(ctx, cur)?;
                l.first().cloned().ok_or_else(|| Esc::Err("FIRST OF AN EMPTY LIST".into()))
            }
            "LAST" => {
                let l = self.want_list(ctx, cur)?;
                l.last().cloned().ok_or_else(|| Esc::Err("LAST OF AN EMPTY LIST".into()))
            }
            "BF" | "BUTFIRST" => {
                let l = self.want_list(ctx, cur)?;
                if l.is_empty() {
                    return Err(Esc::Err("BUTFIRST OF AN EMPTY LIST".into()));
                }
                Ok(Val::List(Rc::new(l[1..].to_vec())))
            }
            "ITEM" => {
                let n = num!() as usize;
                let l = self.want_list(ctx, cur)?;
                if n < 1 || n > l.len() {
                    return Err(Esc::Err(format!("NO ITEM {n} IN A {}-ITEM LIST", l.len())));
                }
                Ok(l[n - 1].clone())
            }
            "COUNT" => {
                let l = self.want_list(ctx, cur)?;
                Ok(Val::Num(l.len() as f64))
            }
            "FPUT" => {
                let v = self.want_val(ctx, cur)?;
                let l = self.want_list(ctx, cur)?;
                let mut out = vec![v];
                out.extend(l.iter().cloned());
                Ok(Val::List(Rc::new(out)))
            }
            "LPUT" => {
                let v = self.want_val(ctx, cur)?;
                let l = self.want_list(ctx, cur)?;
                let mut out = l.to_vec();
                out.push(v);
                Ok(Val::List(Rc::new(out)))
            }
            "EMPTYP" | "EMPTY?" => {
                let l = self.want_list(ctx, cur)?;
                Ok(Val::Num(l.is_empty() as i32 as f64))
            }
            // functions as values
            "FN" | "LAMBDA" => {
                let params = self.want_list(ctx, cur)?;
                let body = self.want_list(ctx, cur)?;
                let names: Vec<String> = params
                    .iter()
                    .map(|p| fmt_val(p).trim_start_matches(':').trim_start_matches('"').to_string())
                    .collect();
                let body_toks = list_to_toks(&body).map_err(Esc::Err)?;
                Ok(Val::Func(Rc::new(ProcDef {
                    name: String::new(),
                    params: names,
                    body: body_toks,
                    src: String::new(),
                })))
            }
            "APPLY" => {
                let f = self.want_fn(ctx, cur)?;
                let args = self.want_list(ctx, cur)?;
                self.apply(ctx, &f, args.to_vec())
            }
            "RUN" => {
                let block = self.want_val(ctx, cur)?;
                Ok(self.run_list_block(ctx, &block)?.unwrap_or(Val::Nothing))
            }
            "MAP" => {
                let f = self.want_fn(ctx, cur)?;
                let l = self.want_list(ctx, cur)?;
                let mut out = Vec::with_capacity(l.len());
                for item in l.iter() {
                    out.push(self.apply(ctx, &f, vec![item.clone()])?);
                }
                Ok(Val::List(Rc::new(out)))
            }
            "FILTER" => {
                let f = self.want_fn(ctx, cur)?;
                let l = self.want_list(ctx, cur)?;
                let mut out = Vec::new();
                for item in l.iter() {
                    if truthy(&self.apply(ctx, &f, vec![item.clone()])?) {
                        out.push(item.clone());
                    }
                }
                Ok(Val::List(Rc::new(out)))
            }
            "REDUCE" => {
                let f = self.want_fn(ctx, cur)?;
                let init = self.want_val(ctx, cur)?;
                let l = self.want_list(ctx, cur)?;
                let mut acc = init;
                for item in l.iter() {
                    acc = self.apply(ctx, &f, vec![acc, item.clone()])?;
                }
                Ok(acc)
            }
            "FOREACH" => {
                let l = self.want_list(ctx, cur)?;
                let f = self.want_fn(ctx, cur)?;
                for item in l.iter() {
                    self.apply(ctx, &f, vec![item.clone()])?;
                }
                Ok(Val::Nothing)
            }
            // user procedures
            _ => {
                let Some(p) = self.procs.get(name).cloned() else {
                    return Err(Esc::Err(format!("I DON'T KNOW HOW TO {name}")));
                };
                let mut args = Vec::with_capacity(p.params.len());
                for _ in 0..p.params.len() {
                    args.push(self.want_val(ctx, cur)?);
                }
                self.apply(ctx, &p, args)
            }
        }
    }
}

// ── the cabinet ──────────────────────────────────────────────────────────

#[derive(Resource)]
struct Lab {
    interp: Interp,
    board: BoardSt,
    lines: Vec<String>,
    input: String,
    history: Vec<String>,
    hist_at: usize,
    defining: Option<Vec<String>>, // buffered TO ... lines
    show_console: bool,
    dirty: bool,
    over: Option<Timer>,
    turtle_ents: Vec<Entity>,
}

impl Lab {
    fn say(&mut self, s: &str) {
        for line in s.split('\n') {
            self.lines.push(line.to_string());
        }
        if self.lines.len() > 80 {
            let cut = self.lines.len() - 80;
            self.lines.drain(0..cut);
        }
        self.dirty = true;
    }
}

#[derive(Component)]
struct Screen;

#[derive(Component)]
struct InputLine;

#[derive(Component)]
struct ConsoleBack;

#[derive(Component)]
struct SegSprite;

#[derive(Component)]
struct Backdrop;

#[derive(Serialize, serde::Deserialize)]
struct WorkspaceDoc {
    v: u32,
    #[serde(default)]
    name: String,
    src: String,
}

use serde::Serialize;

fn page_level() -> Option<WorkspaceDoc> {
    #[derive(serde::Deserialize)]
    struct BlankRef {
        blank: bool,
    }
    #[cfg(target_arch = "wasm32")]
    let raw = js_sys::Reflect::get(&js_sys::global(), &"__ARCADE_LEVEL".into())
        .ok()
        .and_then(|v| v.as_string());
    #[cfg(not(target_arch = "wasm32"))]
    let raw: Option<String> = None;
    if let Some(raw) = raw {
        if serde_json::from_str::<BlankRef>(&raw).map(|b| b.blank).unwrap_or(false) {
            return Some(WorkspaceDoc { v: 1, name: String::new(), src: String::new() });
        }
        if let Ok(doc) = serde_json::from_str::<WorkspaceDoc>(&raw) {
            return Some(doc);
        }
    }
    None
}

pub struct PlotPlugin;

impl Plugin for PlotPlugin {
    fn build(&self, app: &mut App) {
        app.add_systems(OnEnter(Phase::Playing), setup).add_systems(
            Update,
            (typing, spawn_segs, sync_turtles, paint, endgame)
                .chain()
                .run_if(in_state(Phase::Playing))
                .run_if(crate::unpaused),
        );
    }
}

fn setup(mut commands: Commands, mut rng: ResMut<Rng>) {
    commands.spawn((
        Sprite { color: PALETTE[0], custom_size: Some(Vec2::new(744.0, 664.0)), ..default() },
        Transform::from_xyz(0.0, 0.0, 0.1),
        Backdrop,
        GameTag,
    ));
    // Console furniture (bottom strip).
    commands.spawn((
        Sprite { color: Color::srgba(0.0, 0.0, 0.0, 0.62), custom_size: Some(Vec2::new(744.0, 150.0)), ..default() },
        Transform::from_xyz(0.0, -247.0, 30.0),
        ConsoleBack,
        GameTag,
    ));
    commands.spawn((
        Text2d::new(""),
        TextFont { font_size: 11.0, ..default() },
        TextColor(GREEN),
        TextLayout::new_with_justify(JustifyText::Left),
        Anchor::TopLeft,
        Transform::from_xyz(-356.0, -180.0, 31.0),
        Screen,
        GameTag,
    ));
    commands.spawn((
        Text2d::new(""),
        TextFont { font_size: 12.0, ..default() },
        TextColor(WHITE),
        TextLayout::new_with_justify(JustifyText::Left),
        Anchor::BottomLeft,
        Transform::from_xyz(-356.0, -316.0, 31.0),
        InputLine,
        GameTag,
    ));

    let mut lab = Lab {
        interp: Interp::new(),
        board: BoardSt {
            turtles: vec![TurtleSt::hatch()],
            active: 0,
            segs: Vec::new(),
            spawned: 0,
            clear_req: false,
            bg: 0,
            ink_dry_said: false,
            strokes_total: 0,
        },
        lines: Vec::new(),
        input: String::new(),
        history: Vec::new(),
        hist_at: 0,
        defining: None,
        show_console: true,
        dirty: true,
        over: None,
        turtle_ents: Vec::new(),
    };
    lab.say("PLOT DEVICE. THE TURTLE AWAITS INSTRUCTIONS.");
    match page_level() {
        Some(doc) if doc.src.is_empty() => {
            lab.say("A BLANK WORKSPACE. TO NAME :ARG ... END DEFINES. HELP HELPS.");
        }
        Some(doc) => {
            let named = if doc.name.is_empty() { "WORKSPACE".to_string() } else { doc.name.clone() };
            run_source(&mut lab, &mut rng, &doc.src);
            lab.say(&format!("LOADED {named}. POTS LISTS ITS PROCEDURES."));
        }
        None => {
            run_source(&mut lab, &mut rng, DEMO_SRC);
            lab.say("DEMO LIBRARY LOADED. TRY: TREE 90   OR: BURST 24   OR: SPIRO 6 61");
            lab.say("HELP LISTS EVERYTHING. TAB HIDES THIS CONSOLE.");
        }
    }
    commands.insert_resource(lab);
}

fn run_source(lab: &mut Lab, rng: &mut Rng, src: &str) {
    // A workspace file is console lines; TO...END spans lines like typing.
    for line in src.lines() {
        let line = line.trim();
        if !line.is_empty() {
            handle_line(lab, rng, line.to_string(), true);
        }
    }
}

fn define_proc(lab: &mut Lab, src_lines: &[String]) {
    let src = src_lines.join("\n");
    let all = src_lines.join(" ");
    let toks = match lex(&all) {
        Ok(t) => t,
        Err(e) => {
            lab.say(&format!("ERR: {e}"));
            return;
        }
    };
    // TO NAME :A :B ... body ... END
    let mut i = 1;
    let Some(Tok::Word(name)) = toks.get(i) else {
        lab.say("ERR: TO WANTS A NAME");
        return;
    };
    let name = name.clone();
    i += 1;
    let mut params = Vec::new();
    while let Some(Tok::Var(p)) = toks.get(i) {
        params.push(p.clone());
        i += 1;
    }
    let mut body: Vec<Tok> = toks[i..].to_vec();
    match body.last() {
        Some(Tok::Word(w)) if w == "END" => {
            body.pop();
        }
        _ => {
            lab.say("ERR: A DEFINITION ENDS WITH END");
            return;
        }
    }
    lab.interp.procs.insert(
        name.clone(),
        Rc::new(ProcDef { name: name.clone(), params: params.clone(), body, src }),
    );
    stat("procs_defined", 1);
    lab.say(&format!("{name} DEFINED ({} INPUT{})", params.len(), if params.len() == 1 { "" } else { "S" }));
    sfx("place");
}

const HELP: &str = "MOTION: FD BK RT LT SETXY SETX SETY SETH HOME ARC ang r\n\
PEN: PU PD SETPC 0-15 SETRGB r g b SETW n SETSTYLE \"SOLID/\"DASH/\"DOT\n\
SCREEN: CS HT ST SETBG n  (TAB TOGGLES THIS CONSOLE)\n\
TURTLES: NEWTURTLE TELL n ASK n [..] EACH [..] WHO TURTLES\n\
CONTROL: REPEAT n [..] REPCOUNT IF IFELSE WHILE [..] [..] FOR [i a b s] [..] STOP OUTPUT\n\
WORDS: MAKE \"x v  :x  PRINT RANDOM SIN COS SQRT ABS ROUND XCOR YCOR HEADING\n\
LISTS: [..] FIRST BUTFIRST LAST ITEM COUNT FPUT LPUT LIST SE\n\
FUNCTIONS: FN [x] [..] APPLY f [args] MAP FILTER REDUCE FOREACH RUN \"name WORKS TOO\n\
DEFINE: TO NAME :A ... END   PO \"name  POTS  ERASE \"name\n\
WORKSPACE: SAVE \"name (TO THE LEVEL SHELF)   QUIT ENDS THE SESSION";

fn handle_line(lab: &mut Lab, rng: &mut Rng, line: String, quiet: bool) {
    let upper = line.trim().to_uppercase();
    // Definition mode buffers lines until END.
    if let Some(buf) = lab.defining.as_mut() {
        buf.push(line.clone());
        if upper == "END" || upper.ends_with(" END") {
            let src_lines = lab.defining.take().unwrap();
            define_proc(lab, &src_lines);
        }
        return;
    }
    if upper.starts_with("TO ") {
        if upper.ends_with(" END") {
            define_proc(lab, &[line]);
        } else {
            lab.defining = Some(vec![line]);
            if !quiet {
                lab.say("DEFINING... (END FINISHES)");
            }
        }
        return;
    }
    // Console-only commands.
    if upper == "HELP" {
        lab.say(HELP);
        return;
    }
    if upper == "POTS" {
        let mut names: Vec<&str> = lab.interp.procs.keys().map(|s| s.as_str()).collect();
        names.sort_unstable();
        let list = if names.is_empty() { "(NO PROCEDURES YET)".to_string() } else { names.join(" ") };
        lab.say(&format!("PROCEDURES: {list}"));
        return;
    }
    if let Some(rest) = upper.strip_prefix("PO ") {
        let name = rest.trim().trim_start_matches('"');
        match lab.interp.procs.get(name) {
            Some(p) if !p.src.is_empty() => {
                let src = p.src.clone();
                lab.say(&src);
            }
            Some(_) => lab.say("THAT ONE HAS NO SOURCE (A LAMBDA?)"),
            None => lab.say(&format!("NO PROCEDURE CALLED {name}")),
        }
        return;
    }
    if let Some(rest) = upper.strip_prefix("ERASE ") {
        let name = rest.trim().trim_start_matches('"').to_string();
        if lab.interp.procs.remove(&name).is_some() {
            lab.say(&format!("{name} ERASED"));
        } else {
            lab.say(&format!("NO PROCEDURE CALLED {name}"));
        }
        return;
    }
    if let Some(rest) = upper.strip_prefix("SAVE") {
        let name = rest.trim().trim_start_matches('"').to_string();
        let mut names: Vec<&String> = lab.interp.procs.keys().collect();
        names.sort_unstable();
        let src: Vec<String> = names
            .iter()
            .filter_map(|n| lab.interp.procs.get(*n))
            .filter(|p| !p.src.is_empty())
            .map(|p| p.src.clone())
            .collect();
        if src.is_empty() {
            lab.say("NOTHING TO SAVE: DEFINE SOME PROCEDURES FIRST");
            return;
        }
        let doc = WorkspaceDoc { v: 1, name, src: src.join("\n") };
        if let Ok(json) = serde_json::to_string(&doc) {
            save_level(&json);
            stat("workspaces_saved", 1);
            lab.say("WORKSPACE HANDED TO THE SHELF (NAME IT IN THE DIALOG)");
            sfx("clear");
        }
        return;
    }
    if upper == "QUIT" || upper == "BYE" {
        lab.say("PENS DOWN. SCORING THE PLOT...");
        lab.over = Some(Timer::from_seconds(1.6, TimerMode::Once));
        sfx("over");
        return;
    }
    // Everything else runs through the interpreter.
    let toks = match lex(&line) {
        Ok(t) => t,
        Err(e) => {
            lab.say(&format!("ERR: {e}"));
            sfx("buzz");
            return;
        }
    };
    lab.interp.ops = 0;
    lab.interp.prints.clear();
    let strokes_before = lab.board.strokes_total;
    let result = {
        let Lab { interp, board, .. } = lab;
        let mut ctx = Ctx { board, rng };
        interp.run_toks(&mut ctx, &toks)
    };
    let prints: Vec<String> = lab.interp.prints.drain(..).collect();
    for p in prints {
        lab.say(&p);
    }
    match result {
        Ok(()) => {
            if !quiet {
                sfx("tick");
            }
        }
        Err(Esc::Stop) => {}
        Err(Esc::Out(v)) => lab.say(&format!("YOU DON'T SAY WHAT TO DO WITH {}", fmt_val(&v))),
        Err(Esc::Err(e)) => {
            lab.say(&format!("ERR: {e}"));
            stat("programs_crashed", 1);
            sfx("buzz");
        }
    }
    let drawn = lab.board.strokes_total - strokes_before;
    if drawn > 0 {
        stat("strokes_drawn", drawn);
    }
    if lab.board.segs.len() >= SEG_CAP && !lab.board.ink_dry_said {
        lab.board.ink_dry_said = true;
        lab.say("THE INK RAN DRY (SEGMENT LIMIT). CS CLEARS THE PAGE.");
    }
}

fn typing(
    mut events: EventReader<KeyboardInput>,
    keys: Res<ButtonInput<KeyCode>>,
    mut rng: ResMut<Rng>,
    mut lab: ResMut<Lab>,
) {
    if lab.over.is_some() {
        events.clear();
        return;
    }
    if keys.just_pressed(KeyCode::Tab) {
        lab.show_console = !lab.show_console;
        lab.dirty = true;
    }
    if keys.just_pressed(KeyCode::ArrowUp) && !lab.history.is_empty() {
        lab.hist_at = lab.hist_at.saturating_sub(1);
        lab.input = lab.history.get(lab.hist_at).cloned().unwrap_or_default();
        lab.dirty = true;
    }
    if keys.just_pressed(KeyCode::ArrowDown) && !lab.history.is_empty() {
        lab.hist_at = (lab.hist_at + 1).min(lab.history.len());
        lab.input = if lab.hist_at == lab.history.len() {
            String::new()
        } else {
            lab.history[lab.hist_at].clone()
        };
        lab.dirty = true;
    }
    let mut submitted: Option<String> = None;
    for ev in events.read() {
        if !ev.state.is_pressed() {
            continue;
        }
        match &ev.logical_key {
            Key::Character(c) => {
                if lab.input.len() < 160 {
                    for ch in c.chars().filter(|ch| ch.is_ascii() && !ch.is_control()) {
                        lab.input.push(ch);
                    }
                    lab.dirty = true;
                }
            }
            Key::Space => {
                if lab.input.len() < 160 {
                    lab.input.push(' ');
                    lab.dirty = true;
                }
            }
            Key::Backspace => {
                lab.input.pop();
                lab.dirty = true;
            }
            Key::Enter => {
                let line = lab.input.trim().to_string();
                lab.input.clear();
                lab.dirty = true;
                if !line.is_empty() {
                    submitted = Some(line);
                }
            }
            _ => {}
        }
    }
    let Some(line) = submitted else { return };
    lab.say(&format!("> {}", line.to_uppercase()));
    lab.history.push(line.clone());
    lab.hist_at = lab.history.len();
    if !lab.show_console {
        lab.show_console = true; // typing brings the console back
    }
    handle_line(&mut lab, &mut rng, line, false);
}

/// Turns fresh segments into sprites, a budget per frame so huge plots
/// unspool progressively instead of hitching. CS wipes the page.
fn spawn_segs(
    mut commands: Commands,
    mut lab: ResMut<Lab>,
    old: Query<Entity, With<SegSprite>>,
    mut backdrop: Query<&mut Sprite, With<Backdrop>>,
) {
    if lab.board.clear_req {
        lab.board.clear_req = false;
        for e in &old {
            commands.entity(e).despawn();
        }
    }
    if let Ok(mut sp) = backdrop.single_mut() {
        let want = PALETTE[lab.board.bg];
        if sp.color != want {
            sp.color = want;
        }
    }
    let mut budget = 900;
    while lab.board.spawned < lab.board.segs.len() && budget > 0 {
        let s = &lab.board.segs[lab.board.spawned];
        let (a, b, color, width, style) = (s.a, s.b, s.color, s.width, s.style);
        lab.board.spawned += 1;
        let d = b - a;
        let len = d.length();
        if len < 0.01 {
            continue;
        }
        let ang = d.y.atan2(d.x);
        let spawn_piece = |commands: &mut Commands, from: f32, to: f32| {
            let mid = a + d * ((from + to) / 2.0 / len);
            commands.spawn((
                Sprite { color, custom_size: Some(Vec2::new(to - from, width)), ..default() },
                Transform::from_xyz(mid.x, mid.y, 1.0 + (mid.y + 400.0) * 1e-5)
                    .with_rotation(Quat::from_rotation_z(ang)),
                SegSprite,
                GameTag,
            ));
        };
        match style {
            1 => {
                // dashes: 8 on, 6 off
                let mut p = 0.0;
                while p < len && budget > 0 {
                    let q = (p + 8.0).min(len);
                    spawn_piece(&mut commands, p, q);
                    budget -= 1;
                    p += 14.0;
                }
            }
            2 => {
                // dots: 2.4 on, 6 off
                let mut p = 0.0;
                while p < len && budget > 0 {
                    let q = (p + 2.4).min(len);
                    spawn_piece(&mut commands, p, q);
                    budget -= 1;
                    p += 8.4;
                }
            }
            _ => {
                spawn_piece(&mut commands, 0.0, len);
                budget -= 1;
            }
        }
    }
}

fn sync_turtles(
    mut commands: Commands,
    mut lab: ResMut<Lab>,
    mut meshes: ResMut<Assets<Mesh>>,
    mut mats: ResMut<Assets<ColorMaterial>>,
    mut tfs: Query<&mut Transform>,
    mut vis: Query<&mut Visibility>,
    handles: Query<&MeshMaterial2d<ColorMaterial>>,
) {
    // Hatch sprites for new turtles.
    while lab.turtle_ents.len() < lab.board.turtles.len() {
        let e = commands
            .spawn((
                Mesh2d(meshes.add(Triangle2d::new(
                    Vec2::new(0.0, 9.0),
                    Vec2::new(-6.0, -6.0),
                    Vec2::new(6.0, -6.0),
                ))),
                MeshMaterial2d(mats.add(ColorMaterial::from(WHITE))),
                Transform::from_xyz(0.0, 0.0, 25.0),
                GameTag,
            ))
            .id();
        lab.turtle_ents.push(e);
    }
    for (i, t) in lab.board.turtles.iter().enumerate() {
        let e = lab.turtle_ents[i];
        if let Ok(mut tf) = tfs.get_mut(e) {
            tf.translation.x = t.x;
            tf.translation.y = t.y;
            tf.rotation = Quat::from_rotation_z(-t.heading.to_radians());
        }
        if let Ok(mut v) = vis.get_mut(e) {
            *v = if t.visible { Visibility::Inherited } else { Visibility::Hidden };
        }
        if let Ok(h) = handles.get(e) {
            if let Some(m) = mats.get_mut(&h.0) {
                let want = if i == lab.board.active { t.color } else { t.color.with_alpha(0.55) };
                if m.color != want {
                    m.color = want;
                }
            }
        }
    }
}

#[allow(clippy::type_complexity)]
fn paint(
    mut lab: ResMut<Lab>,
    mut screen: Query<&mut Text2d, (With<Screen>, Without<InputLine>)>,
    mut input: Query<&mut Text2d, (With<InputLine>, Without<Screen>)>,
    mut consoles: Query<&mut Visibility, Or<(With<Screen>, With<InputLine>, With<ConsoleBack>)>>,
) {
    if !lab.dirty {
        return;
    }
    lab.dirty = false;
    for mut v in consoles.iter_mut() {
        *v = if lab.show_console { Visibility::Inherited } else { Visibility::Hidden };
    }
    if let Ok(mut t) = screen.single_mut() {
        let n = lab.lines.len();
        let shown = &lab.lines[n.saturating_sub(9)..];
        let s = shown.join("\n");
        if t.0 != s {
            t.0 = s;
        }
    }
    if let Ok(mut t) = input.single_mut() {
        let prompt = if lab.defining.is_some() { "TO>" } else { ">" };
        let s = format!("{prompt} {}_", lab.input.to_uppercase());
        if t.0 != s {
            t.0 = s;
        }
    }
    let _ = (AMBER, CYAN, DIM);
}

fn endgame(
    time: Res<Time>,
    mut lab: ResMut<Lab>,
    mut final_score: ResMut<FinalScore>,
    mut banner: ResMut<crate::EndBanner>,
    mut next: ResMut<NextState<Phase>>,
) {
    if let Some(t) = lab.over.as_mut() {
        if t.tick(time.delta()).finished() {
            final_score.0 = lab.board.strokes_total.min(u32::MAX as u64) as u32;
            banner.0 = Some("PENS DOWN".into());
            next.set(Phase::GameOver);
        }
    }
}
