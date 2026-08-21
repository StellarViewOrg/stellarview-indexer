use soroban_sdk::{contract, contractimpl, vec, Env, String, Vec};

#[contract]
pub struct GoldenContract;

#[contractimpl]
impl GoldenContract {
    pub fn hello(env: Env) -> Vec<String> {
        vec![&env, String::from_str(&env, "Golden")]
    }
}

#[cfg(test)]
mod test {
    use super::*;
    use soroban_sdk::Env;

    #[test]
    fn test_hello() {
        let env = Env::default();
        let contract_id = env.register_contract(None, GoldenContract);
        let client = GoldenContractClient::new(&env, &contract_id);
        assert_eq!(client.hello().len(), 1);
    }
}
