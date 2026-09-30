//! Build inputs for the embedded frontend.

use std::{fs, path::Path};

fn main() {
    let build_dir = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../frontend/build");

    // rust-embed bakes the frontend in at compile time, but cargo does not otherwise know the built
    // SPA is an input. Without this a rebuilt frontend would not trigger a rebuild and the binary
    // would silently ship stale assets.
    println!("cargo:rerun-if-changed={}", build_dir.display());

    // A fresh clone has no frontend/build, and rust-embed fails to compile against a missing
    // directory. Leave a placeholder so `cargo check`, `cargo test` and rust-analyzer work before
    // anyone has run `just build`. A real `just build` overwrites it.
    if !build_dir.join("index.html").exists() {
        let _ = fs::create_dir_all(&build_dir);
        let _ = fs::write(
            build_dir.join("index.html"),
            "<!doctype html><meta charset=\"utf-8\"><title>frontend not built</title>\
             <p>Run <code>just build</code> to embed the real frontend.</p>\n",
        );
    }
}
