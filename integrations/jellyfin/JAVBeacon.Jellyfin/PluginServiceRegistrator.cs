using MediaBrowser.Controller;
using MediaBrowser.Controller.Plugins;
using Microsoft.Extensions.DependencyInjection;

namespace Jellyfin.Plugin.JAVBeacon;

public sealed class PluginServiceRegistrator : IPluginServiceRegistrator
{
    public void RegisterServices(IServiceCollection services, IServerApplicationHost applicationHost)
    {
        services.AddHttpClient(nameof(JAVBeaconClient));
        services.AddSingleton<JAVBeaconClient>();
        services.AddHostedService<PlaybackBridge>();
        services.AddHostedService<LibrarySyncService>();
    }
}
