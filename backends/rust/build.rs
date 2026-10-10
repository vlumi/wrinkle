// The contract is generated from the repository's proto/ at build time.
fn main() -> Result<(), Box<dyn std::error::Error>> {
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
