// The contract is generated from the repository's proto/ at build time.
// protoc comes vendored, so the build needs no system install.
fn main() -> Result<(), Box<dyn std::error::Error>> {
    // The listed files import the rest of proto/; rebuild when any changes.
    println!("cargo:rerun-if-changed=../../proto");
    // SAFETY: build scripts are single-threaded.
    unsafe { std::env::set_var("PROTOC", protoc_bin_vendored::protoc_bin_path()?) };
    tonic_prost_build::configure()
        .build_server(false)
        .compile_protos(
            &[
                "../../proto/wrinkle/v1/frame.proto",
                "../../proto/wrinkle/backend/v1/services.proto",
            ],
            &["../../proto"],
        )?;
    Ok(())
}
