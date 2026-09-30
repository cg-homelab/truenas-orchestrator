//! In-memory TrueNAS used by every test and by `just dev`.
//!
//! M3 backs this with recorded responses from `fixtures/truenas/`, including failure cases. For M0
//! it returns a small fixed state so the server has something to talk to.

use crate::{Dataset, Network, TrueNasApi, TrueNasError};
use async_trait::async_trait;

/// A fake TrueNAS with a fixed in-memory state.
#[derive(Debug, Clone)]
pub struct FakeTrueNas {
    version: String,
    datasets: Vec<Dataset>,
    networks: Vec<Network>,
}

impl Default for FakeTrueNas {
    fn default() -> Self {
        Self {
            version: "TrueNAS-SCALE-24.10.0".to_owned(),
            datasets: Vec::new(),
            // The four networks the homelab contract requires.
            networks: ["home", "proxy", "isolated", "databases"]
                .into_iter()
                .map(|name| Network {
                    name: name.to_owned(),
                })
                .collect(),
        }
    }
}

#[async_trait]
impl TrueNasApi for FakeTrueNas {
    async fn system_version(&self) -> Result<String, TrueNasError> {
        Ok(self.version.clone())
    }

    async fn list_datasets(&self) -> Result<Vec<Dataset>, TrueNasError> {
        Ok(self.datasets.clone())
    }

    async fn list_networks(&self) -> Result<Vec<Network>, TrueNasError> {
        Ok(self.networks.clone())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn fake_reports_the_four_contract_networks() {
        let fake = FakeTrueNas::default();
        let names: Vec<_> = fake
            .list_networks()
            .await
            .unwrap()
            .into_iter()
            .map(|n| n.name)
            .collect();
        assert_eq!(names, ["home", "proxy", "isolated", "databases"]);
    }
}
